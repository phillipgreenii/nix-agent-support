package power

import (
	"errors"
	"testing"
	"time"
)

func TestParsePmsetBatt(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		want    Reading
		wantErr bool
	}{
		{
			name: "battery discharging",
			out:  "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=27459683)\t15%; discharging; 0:41 remaining present: true\n",
			want: Reading{OnBattery: true, Percent: 15},
		},
		{
			name: "ac attached not charging",
			out:  "Now drawing from 'AC Power'\n -InternalBattery-0 (id=27459683)\t80%; AC attached; not charging present: true\n",
			want: Reading{OnBattery: false, Percent: 80},
		},
		{
			name: "ac charging",
			out:  "Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t100%; charged; 0:00 remaining present: true\n",
			want: Reading{OnBattery: false, Percent: 100},
		},
		{
			name: "desktop with no battery",
			out:  "Now drawing from 'AC Power'\n",
			want: Reading{OnBattery: false, Percent: NoPercent},
		},
		{
			name: "ups is not the battery",
			out:  "Now drawing from 'UPS Power'\n -CyberPower (id=2)\t60%; discharging present: true\n",
			want: Reading{OnBattery: false, Percent: NoPercent},
		},
		{
			name:    "battery power but no percentage",
			out:     "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t(no estimate); discharging present: true\n",
			wantErr: true,
		},
		{name: "garbage", out: "pmset: command exploded\n", wantErr: true},
		{name: "empty", out: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePmsetBatt(tc.out)
			if tc.wantErr {
				if !errors.Is(err, ErrUnparseable) {
					t.Fatalf("err = %v, want ErrUnparseable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDarwinReader_CachesWithinTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	runs := 0
	d := &DarwinReader{
		TTL: 15 * time.Second,
		Now: func() time.Time { return now },
		Run: func() ([]byte, error) {
			runs++
			return []byte("Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t42%; discharging present: true\n"), nil
		},
	}
	for i := 0; i < 3; i++ {
		r, err := d.Read()
		if err != nil || r != (Reading{OnBattery: true, Percent: 42}) {
			t.Fatalf("Read %d = %+v, %v", i, r, err)
		}
	}
	if runs != 1 {
		t.Errorf("pmset ran %d times within the TTL, want 1", runs)
	}
	now = now.Add(16 * time.Second)
	if _, err := d.Read(); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Errorf("pmset ran %d times after the TTL, want 2", runs)
	}
}

func TestDarwinReader_RunErrorSurfaces(t *testing.T) {
	boom := errors.New("exec: pmset not found")
	d := &DarwinReader{Run: func() ([]byte, error) { return nil, boom }}
	r, err := d.Read()
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
	if r.OnBattery {
		t.Error("an errored read must not claim OnBattery")
	}
}

func TestNoopReader_IsAlwaysAC(t *testing.T) {
	r, err := NoopReader{}.Read()
	if err != nil || r.OnBattery || r.Percent != NoPercent {
		t.Errorf("NoopReader = %+v, %v; want AC, NoPercent, nil", r, err)
	}
}

func TestFakeReader(t *testing.T) {
	f := NewFakeReader(Reading{OnBattery: true, Percent: 15})
	if r, _ := f.Read(); r != (Reading{OnBattery: true, Percent: 15}) {
		t.Errorf("Read = %+v", r)
	}
	f.SetErr(errors.New("x"))
	if _, err := f.Read(); err == nil {
		t.Error("want error after SetErr")
	}
	f.Set(Reading{Percent: 90})
	if r, err := f.Read(); err != nil || r.Percent != 90 {
		t.Errorf("Set must clear the error: %+v, %v", r, err)
	}
	if f.Reads() != 3 {
		t.Errorf("Reads = %d, want 3", f.Reads())
	}
}
