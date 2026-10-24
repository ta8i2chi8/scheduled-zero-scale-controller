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

// Package schedule decides whether Deployments should be scaled in at a given time.
package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// lookback is how far back to search for the last fired cron.
const lookback = 7 * 24 * time.Hour

// State is the state the matched Deployments should be in.
type State string

const (
	// Normal means the last fired cron is end (or neither fired within lookback).
	Normal State = "Normal"
	// ScaledIn means the last fired cron is start.
	ScaledIn State = "ScaledIn"
)

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Schedule is a parsed pair of start / end crons in a time zone.
type Schedule struct {
	start cron.Schedule
	end   cron.Schedule
	loc   *time.Location
}

// Result is the evaluation of a Schedule at a point in time.
type Result struct {
	State State
	// LastTransition is the last fired cron within lookback, or nil if neither fired.
	LastTransition *time.Time
	// NextTransition is the zero time if neither start nor end ever fires again.
	NextTransition time.Time
}

// Parse parses 5-field cron expressions and an IANA time zone name.
func Parse(start, end, timeZone string) (Schedule, error) {
	if timeZone == "" {
		return Schedule{}, fmt.Errorf("timeZone is empty")
	}
	loc, err := time.LoadLocation(timeZone)
	if err != nil {
		return Schedule{}, fmt.Errorf("invalid timeZone %q: %w", timeZone, err)
	}
	startSchedule, err := parseCron(start)
	if err != nil {
		return Schedule{}, fmt.Errorf("invalid schedule.start: %w", err)
	}
	endSchedule, err := parseCron(end)
	if err != nil {
		return Schedule{}, fmt.Errorf("invalid schedule.end: %w", err)
	}
	return Schedule{start: startSchedule, end: endSchedule, loc: loc}, nil
}

func parseCron(spec string) (cron.Schedule, error) {
	// The parser also accepts a "CRON_TZ=" prefix, which would override timeZone.
	if len(strings.Fields(spec)) != 5 {
		return nil, fmt.Errorf("%q must have exactly 5 fields", spec)
	}
	return parser.Parse(spec)
}

// Evaluate returns the state at now, decided by which of start / end fired last.
func (s Schedule) Evaluate(now time.Time) Result {
	now = now.In(s.loc)
	lastStart := lastFired(s.start, now)
	lastEnd := lastFired(s.end, now)

	result := Result{State: Normal, NextTransition: earlier(s.start.Next(now), s.end.Next(now))}
	switch {
	case lastStart != nil && (lastEnd == nil || lastStart.After(*lastEnd)):
		result.State = ScaledIn
		result.LastTransition = lastStart
	case lastEnd != nil:
		result.LastTransition = lastEnd
	}
	return result
}

// lastFired returns the last time sched fired in (now-lookback, now].
func lastFired(sched cron.Schedule, now time.Time) *time.Time {
	var last *time.Time
	// Next returns the zero time when the cron never fires (e.g. "0 0 30 2 *").
	for t := sched.Next(now.Add(-lookback)); !t.IsZero() && !t.After(now); t = sched.Next(t) {
		fired := t
		last = &fired
	}
	return last
}

// earlier returns the earlier of a and b, ignoring zero times.
func earlier(a, b time.Time) time.Time {
	if b.IsZero() || (!a.IsZero() && a.Before(b)) {
		return a
	}
	return b
}
