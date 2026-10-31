# Datetime tool

`tool.NewDateTimeTool()` returns a context-aware `adapter.Tool` for date/time operations. Inputs are JSON objects passed through the executor. Unknown fields and invalid action/timezone/date/duration inputs return tool errors.

| Action | Required input | Behavior |
|---|---|---|
| `now` | `action` | Current time in the requested timezone |
| `parse` | `date` | Parse a supported textual date |
| `format` | `date` | Render a date in the requested layout/timezone |
| `diff` | `date`, `date2` | Difference from the first date to the second |
| `add` | `duration` | Add a duration to `date`, or current time if omitted |

`timezone` defaults to `UTC` and accepts installed IANA location names such as `America/New_York` or `Asia/Tokyo`.

```json
{"action":"now","timezone":"Asia/Tokyo"}
```

```json
{"action":"parse","date":"2026-10-30"}
```

```json
{"action":"diff","date":"2026-01-01","date2":"2026-01-03"}
```

```json
{"action":"add","date":"2026-01-01","duration":"7d"}
```

`parse` accepts RFC3339/RFC3339Nano, common ISO-like date/time layouts, `YYYY-MM-DD`, US `MM/DD/YYYY`, `DD-Mon-YYYY`, RFC1123, and RFC822. It does not parse Unix timestamp input. `format`, `diff`, and dated `add` accept RFC3339 or `YYYY-MM-DD`.

`format` accepts `iso` (default), `rfc822`, `unix`, `human`, `date`, `time`, or a Go time layout. The `now` output includes `iso`, `unix`, `unix_milli`, `formatted`, `timezone`, and date components. A `diff` output contains total seconds/minutes/hours/days plus a day/hour/minute breakdown; it is not a calendar-month calculation. Differences outside the representable `time.Duration` range return an error.

Durations support Go duration strings such as `30s`, `5m`, `2h`, `-2h`, and an integer `d` suffix. A day is exactly 24 elapsed hours, not a calendar-day addition; week suffixes are not supported. Whole-day magnitudes are limited to 106,751 days to avoid duration overflow. Malformed and out-of-range durations return errors. The tool does not promise locale-dependent natural-language date parsing.
