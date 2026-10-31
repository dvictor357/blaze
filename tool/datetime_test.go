package tool

import (
	"context"
	"testing"
	"time"
)

func TestDateTime_AddDurations(t *testing.T) {
	tl := NewDateTimeTool()
	tests := []struct {
		duration string
		want     string
	}{
		{"30d", "2024-01-31T00:00:00Z"},
		{"-1d", "2023-12-31T00:00:00Z"},
		{"36h", "2024-01-02T12:00:00Z"},
		{"1h30m", "2024-01-01T01:30:00Z"},
		{"106751d", time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC).Add(106751 * 24 * time.Hour).Format(time.RFC3339)},
		{"+106751d", time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC).Add(106751 * 24 * time.Hour).Format(time.RFC3339)},
		{"-106751d", time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC).Add(-106751 * 24 * time.Hour).Format(time.RFC3339)},
	}
	for _, tt := range tests {
		out, err := callTool(t, tl, context.Background(), map[string]string{"action": "add", "date": "2024-01-01", "duration": tt.duration})
		if err != nil || out["result"] != tt.want {
			t.Errorf("add %s = %v (%v), want %s", tt.duration, out["result"], err, tt.want)
		}
	}

	// Day counts that would overflow time.Duration are rejected, not wrapped.
	for _, bad := range []string{"106752d", "+106752d", "-106752d", "99999999d", "-99999999d", "9223372036854775807d", "-9223372036854775808d", "d", "xd", "1.5d", "1d2d", "-1d2d", "1dd", "-1dd", "1d ", "forever"} {
		if out, err := callTool(t, tl, context.Background(), map[string]string{"action": "add", "date": "2024-01-01", "duration": bad}); err == nil {
			t.Errorf("duration %q accepted: %v", bad, out)
		}
	}
}

func TestDateTime_DiffDurationBoundaries(t *testing.T) {
	tl := NewDateTimeTool()
	base := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	const maxDuration = time.Duration(1<<63 - 1)
	const minDuration = time.Duration(-1 << 63)
	tests := []struct {
		name     string
		date1    time.Time
		date2    time.Time
		duration time.Duration
		wantErr  bool
	}{
		{"positive boundary", base, base.Add(maxDuration), maxDuration, false},
		{"negative boundary", base, base.Add(minDuration), minDuration, false},
		{"above positive boundary", base, base.Add(maxDuration).Add(time.Nanosecond), 0, true},
		{"below negative boundary", base, base.Add(minDuration).Add(-time.Nanosecond), 0, true},
		{"positive millennium", base.AddDate(-1000, 0, 0), base, 0, true},
		{"negative millennium", base, base.AddDate(-1000, 0, 0), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := callTool(t, tl, context.Background(), map[string]string{
				"action": "diff",
				"date":   tt.date1.Format(time.RFC3339Nano),
				"date2":  tt.date2.Format(time.RFC3339Nano),
			})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("out-of-range difference accepted: %v", out)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if out["duration_string"] != tt.duration.String() {
				t.Errorf("duration = %v, want %s", out["duration_string"], tt.duration)
			}
		})
	}
}

func TestDateTime_ValidatesItsOwnInput(t *testing.T) {
	tl := NewDateTimeTool()
	bad := []map[string]any{
		{},
		{"action": "explode"},
		{"action": "now", "timezone": "Not/AZone"},
		{"action": "now", "timezone": "../../etc/passwd"},
		{"action": "parse"},
		{"action": "parse", "date": "yesterday-ish"},
		{"action": "diff", "date": "2024-01-01"},
		{"action": "add", "date": "2024-01-01"},
		{"action": "now", "extra": true},
		{"action": 5},
	}
	for _, in := range bad {
		if out, err := callTool(t, tl, context.Background(), in); err == nil {
			t.Errorf("input %v accepted: %v", in, out)
		}
	}
}

func TestDateTime_Operations(t *testing.T) {
	tl := NewDateTimeTool()

	out, err := callTool(t, tl, context.Background(), map[string]string{"action": "now", "timezone": "UTC", "format": "unix"})
	if err != nil || out["timezone"] != "UTC" || out["iso"] == "" || out["formatted"] == "" {
		t.Errorf("now = %v (%v)", out, err)
	}

	out, err = callTool(t, tl, context.Background(), map[string]string{"action": "diff", "date": "2024-01-01", "date2": "2024-01-31"})
	if err != nil || out["total_days"] != float64(30) {
		t.Errorf("diff = %v (%v)", out, err)
	}

	out, err = callTool(t, tl, context.Background(), map[string]string{"action": "parse", "date": "2024-12-25"})
	if err != nil || out["iso"] != "2024-12-25T00:00:00Z" || out["weekday"] != "Wednesday" {
		t.Errorf("parse = %v (%v)", out, err)
	}

	out, err = callTool(t, tl, context.Background(), map[string]string{"action": "format", "date": "2024-12-25", "format": "date"})
	if err != nil || out["formatted"] != "2024-12-25" {
		t.Errorf("format = %v (%v)", out, err)
	}
}
