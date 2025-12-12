package tool

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dvictor357/blaze/adapter"
)

// NewDateTimeTool creates a tool for working with dates and times.
// It can:
// - Get the current time in any timezone
// - Parse date strings
// - Calculate date differences
// - Format dates in different ways
func NewDateTimeTool() adapter.Tool {
	return adapter.NewTool(
		"datetime",
		"Work with dates and times. Get current time, convert timezones, parse dates, calculate differences, and format dates. Use this whenever you need to know the current time or work with dates.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"now", "parse", "format", "diff", "add"},
					"description": "Action to perform: 'now' (current time), 'parse' (string to date), 'format' (date to string), 'diff' (time between dates), 'add' (add duration to date)",
				},
				"timezone": map[string]any{
					"type":        "string",
					"description": "Timezone name (e.g., 'America/New_York', 'UTC', 'Asia/Tokyo'). Default: UTC",
				},
				"date": map[string]any{
					"type":        "string",
					"description": "Date string to parse or format (ISO 8601 format preferred)",
				},
				"date2": map[string]any{
					"type":        "string",
					"description": "Second date for diff operation",
				},
				"format": map[string]any{
					"type":        "string",
					"description": "Output format: 'iso', 'rfc822', 'unix', 'human', or custom Go layout",
				},
				"duration": map[string]any{
					"type":        "string",
					"description": "Duration to add (e.g., '1h', '24h', '7d', '30d', '-2h')",
				},
			},
			"required": []string{"action"},
		},
		func(input json.RawMessage) (any, error) {
			var data struct {
				Action   string `json:"action"`
				Timezone string `json:"timezone"`
				Date     string `json:"date"`
				Date2    string `json:"date2"`
				Format   string `json:"format"`
				Duration string `json:"duration"`
			}
			if err := json.Unmarshal(input, &data); err != nil {
				return nil, fmt.Errorf("invalid input: %w", err)
			}

			// Default timezone
			if data.Timezone == "" {
				data.Timezone = "UTC"
			}

			loc, err := time.LoadLocation(data.Timezone)
			if err != nil {
				return nil, fmt.Errorf("invalid timezone '%s': %w", data.Timezone, err)
			}

			switch data.Action {
			case "now":
				return getCurrentTime(loc, data.Format)

			case "parse":
				if data.Date == "" {
					return nil, fmt.Errorf("date is required for parse action")
				}
				return parseDate(data.Date, loc)

			case "format":
				if data.Date == "" {
					return nil, fmt.Errorf("date is required for format action")
				}
				return formatDate(data.Date, data.Format, loc)

			case "diff":
				if data.Date == "" || data.Date2 == "" {
					return nil, fmt.Errorf("date and date2 are required for diff action")
				}
				return dateDiff(data.Date, data.Date2, loc)

			case "add":
				if data.Duration == "" {
					return nil, fmt.Errorf("duration is required for add action")
				}
				return addDuration(data.Date, data.Duration, loc)

			default:
				return nil, fmt.Errorf("unknown action: %s", data.Action)
			}
		},
	)
}

func getCurrentTime(loc *time.Location, format string) (map[string]any, error) {
	now := time.Now().In(loc)
	return map[string]any{
		"iso":        now.Format(time.RFC3339),
		"unix":       now.Unix(),
		"unix_milli": now.UnixMilli(),
		"formatted":  formatTime(now, format),
		"timezone":   loc.String(),
		"weekday":    now.Weekday().String(),
		"year":       now.Year(),
		"month":      now.Month().String(),
		"day":        now.Day(),
		"hour":       now.Hour(),
		"minute":     now.Minute(),
	}, nil
}

func parseDate(dateStr string, loc *time.Location) (map[string]any, error) {
	// Try multiple formats
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
		"01/02/2006",
		"02-Jan-2006",
		time.RFC1123,
		time.RFC822,
	}

	var parsed time.Time
	var err error
	for _, f := range formats {
		parsed, err = time.ParseInLocation(f, dateStr, loc)
		if err == nil {
			break
		}
	}

	if err != nil {
		return nil, fmt.Errorf("could not parse date '%s': try ISO 8601 format (YYYY-MM-DDTHH:MM:SS)", dateStr)
	}

	return map[string]any{
		"iso":      parsed.Format(time.RFC3339),
		"unix":     parsed.Unix(),
		"valid":    true,
		"weekday":  parsed.Weekday().String(),
		"timezone": loc.String(),
	}, nil
}

func formatDate(dateStr, format string, loc *time.Location) (map[string]any, error) {
	parsed, err := time.ParseInLocation(time.RFC3339, dateStr, loc)
	if err != nil {
		// Try simpler format
		parsed, err = time.ParseInLocation("2006-01-02", dateStr, loc)
		if err != nil {
			return nil, fmt.Errorf("could not parse date: %w", err)
		}
	}

	parsed = parsed.In(loc)

	return map[string]any{
		"formatted": formatTime(parsed, format),
		"original":  dateStr,
		"timezone":  loc.String(),
	}, nil
}

func formatTime(t time.Time, format string) string {
	switch format {
	case "", "iso":
		return t.Format(time.RFC3339)
	case "rfc822":
		return t.Format(time.RFC822)
	case "unix":
		return fmt.Sprintf("%d", t.Unix())
	case "human":
		return t.Format("Monday, January 2, 2006 at 3:04 PM MST")
	case "date":
		return t.Format("2006-01-02")
	case "time":
		return t.Format("15:04:05")
	default:
		// Assume it's a custom Go layout
		return t.Format(format)
	}
}

func dateDiff(date1, date2 string, loc *time.Location) (map[string]any, error) {
	t1, err := time.ParseInLocation(time.RFC3339, date1, loc)
	if err != nil {
		t1, err = time.ParseInLocation("2006-01-02", date1, loc)
		if err != nil {
			return nil, fmt.Errorf("could not parse date1: %w", err)
		}
	}

	t2, err := time.ParseInLocation(time.RFC3339, date2, loc)
	if err != nil {
		t2, err = time.ParseInLocation("2006-01-02", date2, loc)
		if err != nil {
			return nil, fmt.Errorf("could not parse date2: %w", err)
		}
	}

	diff := t2.Sub(t1)

	days := int(diff.Hours() / 24)
	hours := int(diff.Hours()) % 24
	minutes := int(diff.Minutes()) % 60

	return map[string]any{
		"duration_string": diff.String(),
		"total_seconds":   diff.Seconds(),
		"total_minutes":   diff.Minutes(),
		"total_hours":     diff.Hours(),
		"total_days":      diff.Hours() / 24,
		"breakdown": map[string]int{
			"days":    days,
			"hours":   hours,
			"minutes": minutes,
		},
	}, nil
}

func addDuration(dateStr, duration string, loc *time.Location) (map[string]any, error) {
	var baseTime time.Time
	var err error

	if dateStr == "" {
		baseTime = time.Now().In(loc)
	} else {
		baseTime, err = time.ParseInLocation(time.RFC3339, dateStr, loc)
		if err != nil {
			baseTime, err = time.ParseInLocation("2006-01-02", dateStr, loc)
			if err != nil {
				return nil, fmt.Errorf("could not parse date: %w", err)
			}
		}
	}

	// Parse duration - support days (not native to Go)
	var dur time.Duration
	if len(duration) > 1 && duration[len(duration)-1] == 'd' {
		// Handle days
		var days int
		_, err := fmt.Sscanf(duration, "%dd", &days)
		if err != nil {
			return nil, fmt.Errorf("invalid duration: %w", err)
		}
		dur = time.Duration(days) * 24 * time.Hour
	} else {
		dur, err = time.ParseDuration(duration)
		if err != nil {
			return nil, fmt.Errorf("invalid duration '%s': use formats like '1h', '30m', '7d'", duration)
		}
	}

	result := baseTime.Add(dur)

	return map[string]any{
		"original": baseTime.Format(time.RFC3339),
		"result":   result.Format(time.RFC3339),
		"duration": duration,
		"timezone": loc.String(),
		"unix":     result.Unix(),
	}, nil
}
