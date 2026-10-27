// Package cron stores and runs the scheduled jobs.
package cron

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	robfig "github.com/robfig/cron/v3"
)

// Schedule gives the run times of a job.
type Schedule interface {
	// Next returns the first run time after after, or the zero time when no further run exists.
	Next(after time.Time) time.Time
	// OneShot is true for a schedule that runs one time only.
	OneShot() bool
	// String returns the schedule in a form that Parse accepts.
	String() string
}

const formatHelp = "use 30m, every 2h, a 5-field cron expression, or an ISO time"

var (
	durationRE  = regexp.MustCompile(`^(\d+)\s*(m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days)$`)
	cronFieldRE = regexp.MustCompile(`^[0-9A-Za-z*,/?-]+$`)
	isoDateRE   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)
)

// Parse reads a schedule. The formats are (Hermes):
//   - 30m, 2h, or 1d: an interval
//   - every 30m or every 2h: the same interval
//   - a 5-field cron expression such as "0 1 * * *", in loc
//   - an ISO time such as 2026-11-01T09:00, with or without an offset: a one-shot run.
//     A time without an offset is read in loc.
//
// Parse refuses a one-shot time that is not after now. loc is the time zone of the host or
// of TZ. A nil loc means UTC.
func Parse(spec string, now time.Time, loc *time.Location) (Schedule, error) {
	s, err := parse(spec, loc)
	if err != nil {
		return nil, err
	}
	switch v := s.(type) {
	case oneShot:
		if !v.at.After(now) {
			return nil, fmt.Errorf("schedule: %s is in the past (now is %s). Give a future time",
				v.at.Format(time.RFC3339), now.In(v.at.Location()).Format(time.RFC3339))
		}
	case cronSchedule:
		if v.Next(now).IsZero() {
			return nil, fmt.Errorf("schedule: the cron expression %q never runs: %s", spec, formatHelp)
		}
	}
	return s, nil
}

// parse reads spec without the check for a past time. The job store uses it to read back a
// stored schedule.
func parse(spec string, loc *time.Location) (Schedule, error) {
	if loc == nil {
		loc = time.UTC
	}
	spec = strings.TrimSpace(spec)
	lower := strings.ToLower(spec)
	if rest, ok := strings.CutPrefix(lower, "every "); ok {
		return parseInterval(strings.TrimSpace(rest), spec)
	}
	if durationRE.MatchString(lower) {
		return parseInterval(lower, spec)
	}
	if fields := strings.Fields(spec); len(fields) == 5 && allMatch(fields, cronFieldRE) {
		sched, err := robfig.ParseStandard(spec)
		if err != nil {
			return nil, fmt.Errorf("schedule: bad cron expression %q (%v): %s", spec, err, formatHelp)
		}
		ss, ok := sched.(*robfig.SpecSchedule)
		if !ok {
			return nil, fmt.Errorf("schedule: bad cron expression %q: %s", spec, formatHelp)
		}
		// Set the zone on the schedule itself. Then Next does not depend on the zone of its
		// argument, and the parser never uses time.Local.
		ss.Location = loc
		return cronSchedule{spec: strings.Join(fields, " "), s: ss, loc: loc}, nil
	}
	if isoDateRE.MatchString(spec) {
		at, err := parseISO(spec, loc)
		if err != nil {
			return nil, fmt.Errorf("schedule: bad ISO time %q: %s", spec, formatHelp)
		}
		return oneShot{at: at}, nil
	}
	return nil, fmt.Errorf("schedule: %s (got %q)", formatHelp, spec)
}

func allMatch(fields []string, re *regexp.Regexp) bool {
	for _, f := range fields {
		if !re.MatchString(f) {
			return false
		}
	}
	return true
}

func parseInterval(s, spec string) (Schedule, error) {
	m := durationRE.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("schedule: bad interval %q: %s", spec, formatHelp)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 || n > 366*24*60 {
		return nil, fmt.Errorf("schedule: bad interval %q, it must be from 1m to 366d: %s", spec, formatHelp)
	}
	minutes := n
	switch m[2][0] {
	case 'h':
		minutes = n * 60
	case 'd':
		minutes = n * 24 * 60
	}
	if minutes > 366*24*60 {
		return nil, fmt.Errorf("schedule: bad interval %q, it must be from 1m to 366d: %s", spec, formatHelp)
	}
	return interval{d: time.Duration(minutes) * time.Minute}, nil
}

var isoLocalLayouts = []string{
	"2006-01-02T15:04",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02",
}

var isoOffsetLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04Z07:00",
}

// parseISO reads an ISO time. A time without an offset is read in loc.
func parseISO(s string, loc *time.Location) (time.Time, error) {
	if len(s) > 10 && s[10] == ' ' {
		s = s[:10] + "T" + s[11:]
	}
	for _, layout := range isoOffsetLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.In(loc), nil
		}
	}
	for _, layout := range isoLocalLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("not an ISO time")
}

// interval runs every d. The first run is d after the creation time.
type interval struct{ d time.Duration }

func (i interval) Next(after time.Time) time.Time { return after.Add(i.d) }
func (i interval) OneShot() bool                  { return false }
func (i interval) String() string {
	m := int(i.d / time.Minute)
	switch {
	case m%(24*60) == 0:
		return fmt.Sprintf("every %dd", m/(24*60))
	case m%60 == 0:
		return fmt.Sprintf("every %dh", m/60)
	}
	return fmt.Sprintf("every %dm", m)
}

// cronSchedule is a 5-field cron expression in a fixed time zone.
type cronSchedule struct {
	spec string
	s    *robfig.SpecSchedule
	loc  *time.Location
}

func (c cronSchedule) Next(after time.Time) time.Time {
	n := c.s.Next(after)
	if n.IsZero() {
		return n
	}
	return n.In(c.loc)
}
func (c cronSchedule) OneShot() bool  { return false }
func (c cronSchedule) String() string { return c.spec }

// oneShot runs one time, at at.
type oneShot struct{ at time.Time }

func (o oneShot) Next(after time.Time) time.Time {
	if after.Before(o.at) {
		return o.at
	}
	return time.Time{}
}
func (o oneShot) OneShot() bool  { return true }
func (o oneShot) String() string { return o.at.Format(time.RFC3339) }
