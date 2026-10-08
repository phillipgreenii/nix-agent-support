package civil

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseDate(t *testing.T) {
	tests := []struct {
		in      string
		want    Date
		wantErr bool
	}{
		{in: "2026-10-07", want: Date{2026, time.October, 7}},
		{in: "2028-02-29", want: Date{2028, time.February, 29}},
		{in: "2026-13-01", wantErr: true},
		{in: "2026-02-30", wantErr: true},
		{in: "2027-02-29", wantErr: true},
		{in: "2026-00-10", wantErr: true},
		{in: "2026-10-00", wantErr: true},
		{in: "2026-1-7", wantErr: true},
		{in: "", wantErr: true},
		{in: "2026-10-07 ", wantErr: true},
		{in: " 2026-10-07", wantErr: true},
		{in: "2026/10/07", wantErr: true},
		{in: "2026-10-07T00:00:00Z", wantErr: true},
		{in: "+026-10-07", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseDate(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseDate(%q) = %v, want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDate(%q) error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseDate(%q) = %v, want %v", tt.in, got, tt.want)
			}
			if got.String() != tt.in {
				t.Fatalf("String() = %q, want %q", got.String(), tt.in)
			}
		})
	}
}

func TestDateArithmetic(t *testing.T) {
	addDays := []struct {
		from Date
		n    int
		want Date
	}{
		{Date{2026, time.October, 7}, 0, Date{2026, time.October, 7}},
		{Date{2026, time.October, 7}, 1, Date{2026, time.October, 8}},
		{Date{2026, time.October, 31}, 1, Date{2026, time.November, 1}},
		{Date{2026, time.December, 31}, 1, Date{2027, time.January, 1}},
		{Date{2027, time.January, 1}, -1, Date{2026, time.December, 31}},
		{Date{2028, time.February, 28}, 1, Date{2028, time.February, 29}},
		{Date{2028, time.February, 29}, 1, Date{2028, time.March, 1}},
		{Date{2027, time.February, 28}, 1, Date{2027, time.March, 1}},
		{Date{2026, time.October, 7}, 365, Date{2027, time.October, 7}},
		{Date{2026, time.October, 7}, -279, Date{2026, time.January, 1}},
	}
	for _, tt := range addDays {
		got := tt.from.AddDays(tt.n)
		if got != tt.want {
			t.Errorf("%v.AddDays(%d) = %v, want %v", tt.from, tt.n, got, tt.want)
		}
		if back := tt.want.DaysUntil(tt.from); back != -tt.n {
			t.Errorf("%v.DaysUntil(%v) = %d, want %d", tt.want, tt.from, back, -tt.n)
		}
		if fwd := tt.from.DaysUntil(tt.want); fwd != tt.n {
			t.Errorf("%v.DaysUntil(%v) = %d, want %d", tt.from, tt.want, fwd, tt.n)
		}
	}

	if got := (Date{2026, time.October, 7}).Weekday(); got != time.Wednesday {
		t.Errorf("Weekday of 2026-10-07 = %v, want Wednesday", got)
	}

	a, b := Date{2026, time.October, 7}, Date{2026, time.October, 8}
	if a.Compare(b) >= 0 || b.Compare(a) <= 0 || a.Compare(a) != 0 {
		t.Errorf("Compare ordering wrong: a<b=%d b>a=%d a=a=%d", a.Compare(b), b.Compare(a), a.Compare(a))
	}
	if (Date{2025, time.December, 31}).Compare(Date{2026, time.January, 1}) >= 0 {
		t.Errorf("Compare must order by year first")
	}
}

func TestDateJSONRoundTrip(t *testing.T) {
	type holder struct {
		D Date `json:"d"`
	}
	b, err := json.Marshal(holder{D: Date{2026, time.October, 7}})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"d":"2026-10-07"}` {
		t.Fatalf("Marshal = %s", b)
	}
	var h holder
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatal(err)
	}
	if h.D != (Date{2026, time.October, 7}) {
		t.Fatalf("Unmarshal = %v", h.D)
	}
	if err := json.Unmarshal([]byte(`{"d":"2026-02-30"}`), &h); err == nil {
		t.Fatal("Unmarshal accepted a date that does not exist")
	}
}

func TestParseTimeOfDay(t *testing.T) {
	tests := []struct {
		in      string
		want    TimeOfDay
		wantErr bool
	}{
		{in: "00:00", want: TimeOfDay{0, 0}},
		{in: "09:30", want: TimeOfDay{9, 30}},
		{in: "23:59", want: TimeOfDay{23, 59}},
		{in: "24:00", wantErr: true},
		{in: "9:30", wantErr: true},
		{in: "09:60", wantErr: true},
		{in: "09:30:00", wantErr: true},
		{in: "", wantErr: true},
		{in: "09:3", wantErr: true},
		{in: "09-30", wantErr: true},
		{in: " 09:30", wantErr: true},
		{in: "-1:30", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseTimeOfDay(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseTimeOfDay(%q) = %v, want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTimeOfDay(%q) error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseTimeOfDay(%q) = %v, want %v", tt.in, got, tt.want)
			}
			if got.String() != tt.in {
				t.Fatalf("String() = %q, want %q", got.String(), tt.in)
			}
		})
	}
}

func TestTimeOfDayJSONRoundTrip(t *testing.T) {
	type holder struct {
		T TimeOfDay `json:"t"`
	}
	b, err := json.Marshal(holder{T: TimeOfDay{9, 5}})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"t":"09:05"}` {
		t.Fatalf("Marshal = %s", b)
	}
	var h holder
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatal(err)
	}
	if h.T != (TimeOfDay{9, 5}) {
		t.Fatalf("Unmarshal = %v", h.T)
	}
	if err := json.Unmarshal([]byte(`{"t":"24:00"}`), &h); err == nil {
		t.Fatal("Unmarshal accepted 24:00")
	}
}

func TestParseWeekday(t *testing.T) {
	valid := map[string]time.Weekday{
		"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
		"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
	}
	for in, want := range valid {
		got, err := ParseWeekday(in)
		if err != nil {
			t.Errorf("ParseWeekday(%q) error: %v", in, err)
		}
		if got != want {
			t.Errorf("ParseWeekday(%q) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"", "Mon", "MON", "monday", "mo", "mon ", "0", "tues"} {
		if got, err := ParseWeekday(in); err == nil {
			t.Errorf("ParseWeekday(%q) = %v, want an error", in, got)
		}
	}
}
