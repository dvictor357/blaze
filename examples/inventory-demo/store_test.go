package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dvictor357/blaze/adapter"
)

func testStore(t *testing.T) *store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "inventory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}

func testExecutor(t *testing.T, s *store) *adapter.Executor {
	t.Helper()
	ex, err := newExecutor(s)
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

func getState(t *testing.T, s *store) inventoryState {
	t.Helper()
	state, err := s.state(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func executeReservation(ex *adapter.Executor, ctx context.Context, in reserveInput) (adapter.Result, error) {
	input, err := json.Marshal(in)
	if err != nil {
		return adapter.Result{}, err
	}
	results, err := ex.Execute(ctx, []adapter.Call{{ID: "reservation", Name: "reserve_inventory", Input: input}})
	if err != nil {
		return adapter.Result{}, err
	}
	return results[0], nil
}

func TestConcurrentReservationsNeverOversell(t *testing.T) {
	s := testStore(t)
	ex := testExecutor(t, s)
	const attempts = 24
	start := make(chan struct{})
	results := make(chan adapter.Result, attempts)
	errors := make(chan error, attempts)
	var workers sync.WaitGroup
	for i := range attempts {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			<-start
			result, err := executeReservation(ex, context.Background(), reserveInput{"KB-001", 1, fmt.Sprintf("ORDER-%02d", i)})
			if err != nil {
				errors <- err
				return
			}
			results <- result
		}(i)
	}
	close(start)
	workers.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	successes, rejected := 0, 0
	for result := range results {
		if result.OK {
			successes++
		} else if result.Error != nil && strings.Contains(result.Error.Message, "insufficient_stock") {
			rejected++
		} else {
			t.Errorf("unexpected reservation result: %+v", result)
		}
	}
	state := getState(t, s)
	if successes != 8 || rejected != attempts-8 || state.Product.Available != 0 || state.Product.Reserved != 8 || len(state.Reservations) != 8 {
		t.Fatalf("oversell or lost reservation: success=%d rejected=%d state=%+v", successes, rejected, state)
	}
	if len(state.Events) != attempts {
		t.Fatalf("each attempt must be audited once: got %d events", len(state.Events))
	}
	quantity := 0
	for _, reservation := range state.Reservations {
		quantity += reservation.Quantity
	}
	if quantity != state.Product.Reserved {
		t.Fatalf("reservation total %d differs from reserved %d", quantity, state.Product.Reserved)
	}
}

func TestDuplicateOrderUsesApplicationIdempotency(t *testing.T) {
	s := testStore(t)
	ex := testExecutor(t, s)
	in := reserveInput{"KB-001", 3, "ORD-1042"}
	first, err := executeReservation(ex, context.Background(), in)
	if err != nil || !first.OK {
		t.Fatalf("initial reservation: result=%+v err=%v", first, err)
	}
	mcpContext := context.WithValue(context.Background(), sourceKey{}, "MCP")
	replay, err := executeReservation(ex, mcpContext, in)
	if err != nil || !replay.OK {
		t.Fatalf("identical replay: result=%+v err=%v", replay, err)
	}
	var original, repeated reserveOutput
	if err := json.Unmarshal(first.Output, &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(replay.Output, &repeated); err != nil {
		t.Fatal(err)
	}
	if original.Idempotent || !repeated.Idempotent || original.Reservation.ID != repeated.Reservation.ID {
		t.Fatalf("replay did not return original reservation: first=%+v repeated=%+v", original, repeated)
	}
	in.Quantity = 2
	conflict, err := executeReservation(ex, mcpContext, in)
	if err != nil || conflict.OK || conflict.Error == nil || !strings.Contains(conflict.Error.Message, "duplicate_order") {
		t.Fatalf("conflicting order reuse: result=%+v err=%v", conflict, err)
	}
	state := getState(t, s)
	if state.Product.Available != 5 || state.Product.Reserved != 3 || len(state.Reservations) != 1 || len(state.Events) != 3 {
		t.Fatalf("duplicate order changed stock or audit count: %+v", state)
	}
	if state.Events[0].Code != "duplicate_order" || state.Events[1].Code != "already_reserved" || state.Events[1].Source != "MCP" || state.Reservations[0].Source != "HTTP" {
		t.Fatalf("unexpected idempotency audit: %+v", state)
	}
}

func TestRejectedRequestsLeaveStockUnchanged(t *testing.T) {
	s := testStore(t)
	handler, err := newHandler(s, "test-token", "127.0.0.1:18084")
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(input string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "http://localhost/api/invoke", strings.NewReader(input))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	accepted := invoke(`{"calls":[{"id":"seed","name":"reserve_inventory","input":{"sku":"KB-001","quantity":3,"order_id":"ORD-1042"}}],"stream":false}`)
	if accepted.Code != http.StatusOK {
		t.Fatalf("initial reservation: %d %s", accepted.Code, accepted.Body)
	}
	before := getState(t, s)
	cases := []struct {
		name  string
		input string
		code  string
	}{
		{"negative quantity", `{"sku":"KB-001","quantity":-2,"order_id":"ORD-BAD"}`, adapter.CodeInvalidArguments},
		{"insufficient stock", `{"sku":"KB-001","quantity":10,"order_id":"ORD-OVER"}`, adapter.CodeHandler},
		{"quantity over limit", `{"sku":"KB-001","quantity":101,"order_id":"ORD-LIMIT"}`, adapter.CodeInvalidArguments},
		{"unknown SKU", `{"sku":"OTHER","quantity":1,"order_id":"ORD-SKU"}`, adapter.CodeInvalidArguments},
		{"empty order", `{"sku":"KB-001","quantity":1,"order_id":""}`, adapter.CodeInvalidArguments},
		{"extra property", `{"sku":"KB-001","quantity":1,"order_id":"ORD-EXTRA","source":"MCP"}`, adapter.CodeInvalidArguments},
	}
	for i, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := invoke(`{"calls":[{"id":"rejected","name":"reserve_inventory","input":` + test.input + `}],"stream":false}`)
			var body adapter.InvokeResponse
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Results) != 1 {
				t.Fatalf("unexpected adapter response: %d %s", response.Code, response.Body)
			}
			result := body.Results[0]
			if result.OK || result.Error == nil || result.Error.Code != test.code {
				t.Fatalf("unexpected rejection: %+v", result)
			}
			state := getState(t, s)
			if state.Product != before.Product || len(state.Reservations) != 1 || len(state.Events) != i+2 || state.Events[0].Status != "rejected" {
				t.Fatalf("rejected request mutated stock or audited twice: %+v", state)
			}
		})
	}
}

func TestReservationPersistsWithoutReseeding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	_, reserveErr := s.reserve(context.Background(), reserveInput{"KB-001", 3, "ORD-1042"}, "HTTP")
	closeErr := s.db.Close()
	if reserveErr != nil || closeErr != nil {
		t.Fatalf("reserve=%v close=%v", reserveErr, closeErr)
	}
	reopened, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.db.Close()
	state := getState(t, reopened)
	if state.Product.Available != 5 || state.Product.Reserved != 3 || len(state.Reservations) != 1 || len(state.Events) != 1 {
		t.Fatalf("reopening reset persisted inventory: %+v", state)
	}
}
