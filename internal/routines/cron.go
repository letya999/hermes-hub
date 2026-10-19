package routines

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

func NextExpression(expr string, loc *time.Location, from time.Time) (time.Time, error) {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "once:") {
		due, err := time.Parse(time.RFC3339, strings.TrimPrefix(expr, "once:"))
		if err != nil || !due.After(from) {
			return time.Time{}, errors.New("invalid once expression")
		}
		return due.UTC(), nil
	}
	return NextCron(expr, loc, from)
}

func NextCron(expr string, loc *time.Location, from time.Time) (time.Time, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return time.Time{}, errors.New("cron expression must have 5 fields")
	}
	minute, err1 := parseCronField(fields[0], 0, 59)
	hour, err2 := parseCronField(fields[1], 0, 23)
	dom, err3 := parseCronField(fields[2], 1, 31)
	month, err4 := parseCronField(fields[3], 1, 12)
	dow, err5 := parseCronField(fields[4], 0, 6)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
		return time.Time{}, errors.New("invalid cron field")
	}
	t := from.In(loc).Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(400 * 24 * time.Hour)
	for !t.After(limit) {
		if minute[t.Minute()] && hour[t.Hour()] && month[int(t.Month())] {
			if fields[2] != "*" && fields[4] != "*" {
				if dom[t.Day()] || dow[int(t.Weekday())] {
					return t.UTC(), nil
				}
			} else if (fields[2] == "*" || dom[t.Day()]) && (fields[4] == "*" || dow[int(t.Weekday())]) {
				return t.UTC(), nil
			}
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, errors.New("no upcoming cron occurrence")
}

func parseCronField(field string, min, max int) ([]bool, error) {
	ok := make([]bool, max+1)
	if field == "*" {
		for i := min; i <= max; i++ {
			ok[i] = true
		}
		return ok, nil
	}
	for _, part := range strings.Split(field, ",") {
		step := 1
		rangePart := part
		if strings.Contains(part, "/") {
			bits := strings.SplitN(part, "/", 2)
			rangePart = bits[0]
			n, err := strconv.Atoi(bits[1])
			if err != nil || n <= 0 {
				return nil, errors.New("invalid step")
			}
			step = n
		}
		start, end := min, max
		if rangePart != "*" {
			if strings.Contains(rangePart, "-") {
				bits := strings.SplitN(rangePart, "-", 2)
				a, err1 := strconv.Atoi(bits[0])
				b, err2 := strconv.Atoi(bits[1])
				if err1 != nil || err2 != nil || a < min || b > max || a > b {
					return nil, errors.New("invalid range")
				}
				start, end = a, b
			} else {
				a, err := strconv.Atoi(rangePart)
				if err != nil || a < min || a > max {
					return nil, errors.New("invalid value")
				}
				start, end = a, a
			}
		}
		for i := start; i <= end; i += step {
			ok[i] = true
		}
	}
	return ok, nil
}
