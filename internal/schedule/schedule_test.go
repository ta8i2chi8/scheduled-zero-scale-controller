/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package schedule

import (
	"testing"
	"time"
)

const (
	weekdayStart = "0 21 * * 1-5"
	weekdayEnd   = "0 8 * * 1-5"
	dailyStart   = "0 21 * * *"
	dailyEnd     = "0 8 * * *"
	tokyoName    = "Asia/Tokyo"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestEvaluate(t *testing.T) {
	tokyo := mustLoad(t, tokyoName)
	jst := func(month time.Month, day, hour, minute int) time.Time {
		return time.Date(2026, month, day, hour, minute, 0, 0, tokyo)
	}

	tests := []struct {
		name     string
		start    string
		end      string
		timeZone string
		now      time.Time
		want     State
		wantLast *time.Time
		wantNext time.Time
	}{
		{
			name: "weekday daytime", start: weekdayStart, end: weekdayEnd, timeZone: tokyoName,
			now: jst(10, 21, 12, 0), want: Normal, wantLast: new(jst(10, 21, 8, 0)), wantNext: jst(10, 21, 21, 0),
		},
		{
			name: "weekday night", start: weekdayStart, end: weekdayEnd, timeZone: tokyoName,
			now: jst(10, 21, 23, 0), want: ScaledIn, wantLast: new(jst(10, 21, 21, 0)), wantNext: jst(10, 22, 8, 0),
		},
		{
			name: "weekend", start: weekdayStart, end: weekdayEnd, timeZone: tokyoName,
			now: jst(10, 24, 15, 0), want: ScaledIn, wantLast: new(jst(10, 23, 21, 0)), wantNext: jst(10, 26, 8, 0),
		},
		{
			name: "monday morning", start: weekdayStart, end: weekdayEnd, timeZone: tokyoName,
			now: jst(10, 26, 9, 0), want: Normal, wantLast: new(jst(10, 26, 8, 0)), wantNext: jst(10, 26, 21, 0),
		},
		{
			name: "exactly at start", start: weekdayStart, end: weekdayEnd, timeZone: tokyoName,
			now: jst(10, 21, 21, 0), want: ScaledIn, wantLast: new(jst(10, 21, 21, 0)), wantNext: jst(10, 22, 8, 0),
		},
		{
			name: "exactly at end", start: weekdayStart, end: weekdayEnd, timeZone: tokyoName,
			now: jst(10, 22, 8, 0), want: Normal, wantLast: new(jst(10, 22, 8, 0)), wantNext: jst(10, 22, 21, 0),
		},
		{
			name: "just before start", start: weekdayStart, end: weekdayEnd, timeZone: tokyoName,
			now: jst(10, 21, 20, 59).Add(59 * time.Second), want: Normal, wantLast: new(jst(10, 21, 8, 0)), wantNext: jst(10, 21, 21, 0),
		},
		{
			name: "neither fired within 7 days", start: "0 21 1 * *", end: "0 8 2 * *", timeZone: tokyoName,
			now: jst(10, 21, 12, 0), want: Normal, wantLast: nil, wantNext: jst(11, 1, 21, 0),
		},
		{
			name: "only start fired within 7 days", start: "0 21 20 * *", end: "0 8 2 * *", timeZone: tokyoName,
			now: jst(10, 21, 12, 0), want: ScaledIn, wantLast: new(jst(10, 20, 21, 0)), wantNext: jst(11, 2, 8, 0),
		},
		{
			name: "start and end at the same time", start: dailyStart, end: dailyStart, timeZone: tokyoName,
			now: jst(10, 21, 22, 0), want: Normal, wantLast: new(jst(10, 21, 21, 0)), wantNext: jst(10, 22, 21, 0),
		},
		{
			// 13:00 UTC is 22:00 in Tokyo.
			name: "interpreted in Asia/Tokyo", start: weekdayStart, end: weekdayEnd, timeZone: tokyoName,
			now: time.Date(2026, 10, 21, 13, 0, 0, 0, time.UTC), want: ScaledIn, wantLast: new(jst(10, 21, 21, 0)), wantNext: jst(10, 22, 8, 0),
		},
		{
			name: "interpreted in UTC", start: weekdayStart, end: weekdayEnd, timeZone: "UTC",
			now: time.Date(2026, 10, 21, 13, 0, 0, 0, time.UTC), want: Normal,
			wantLast: new(time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)), wantNext: time.Date(2026, 10, 21, 21, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Parse(tt.start, tt.end, tt.timeZone)
			if err != nil {
				t.Fatal(err)
			}
			got := s.Evaluate(tt.now)
			if got.State != tt.want {
				t.Errorf("State = %s, want %s", got.State, tt.want)
			}
			switch {
			case tt.wantLast == nil && got.LastTransition != nil:
				t.Errorf("LastTransition = %v, want nil", *got.LastTransition)
			case tt.wantLast != nil && (got.LastTransition == nil || !got.LastTransition.Equal(*tt.wantLast)):
				t.Errorf("LastTransition = %v, want %v", got.LastTransition, *tt.wantLast)
			}
			if !got.NextTransition.Equal(tt.wantNext) {
				t.Errorf("NextTransition = %v, want %v", got.NextTransition, tt.wantNext)
			}
		})
	}
}

func TestEvaluateNeverFires(t *testing.T) {
	s, err := Parse("0 0 30 2 *", "0 0 31 2 *", tokyoName)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Evaluate(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	if got.State != Normal || got.LastTransition != nil || !got.NextTransition.IsZero() {
		t.Errorf("got %+v, want Normal with no transitions", got)
	}
}

func TestParseInvalid(t *testing.T) {
	tests := []struct {
		name     string
		start    string
		end      string
		timeZone string
	}{
		{name: "invalid start", start: "61 * * * *", end: dailyEnd, timeZone: tokyoName},
		{name: "invalid end", start: dailyStart, end: "not a cron", timeZone: tokyoName},
		{name: "6 fields", start: "0 0 21 * * *", end: dailyEnd, timeZone: tokyoName},
		{name: "descriptor", start: "@daily", end: dailyEnd, timeZone: tokyoName},
		{name: "CRON_TZ prefix", start: "CRON_TZ=UTC 0 21 * * *", end: dailyEnd, timeZone: tokyoName},
		{name: "invalid time zone", start: dailyStart, end: dailyEnd, timeZone: "Asia/Nowhere"},
		{name: "empty time zone", start: dailyStart, end: dailyEnd, timeZone: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.start, tt.end, tt.timeZone); err == nil {
				t.Error("Parse succeeded, want error")
			}
		})
	}
}
