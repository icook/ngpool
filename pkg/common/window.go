package common

import "time"

type sample struct {
	time   time.Time
	amount float64
}

type Window struct {
	samples []*sample
	head    int
}

func NewWindow(size int) Window {
	return Window{
		samples: make([]*sample, size),
	}
}

func (w *Window) Add(val float64) {
	w.samples[w.head] = &sample{time.Now(), val}
	w.head += 1
	if w.head >= len(w.samples) {
		w.head = 0
	}
}

func (w *Window) RateSecond() float64 {
	return w.Rate(time.Second)
}

func (w *Window) RateMinute() float64 {
	return w.Rate(time.Minute)
}

func (w *Window) Rate(interval time.Duration) float64 {
	var total float64
	var newest *time.Time
	for _, sam := range w.samples {
		if sam == nil {
			continue
		}
		if newest == nil || sam.time.Before(*newest) {
			newest = &sam.time
		}
		total += sam.amount
	}
	if newest == nil {
		return 0
	}
	periods := float64(time.Now().Sub(*newest)) / float64(interval)

	return total / periods
}
