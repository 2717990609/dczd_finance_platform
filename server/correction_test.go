package main

import (
	"reflect"
	"testing"
)

func TestRecommendationDeltaUsesRecordedNetAndSkipsOnlyVerifiedUnreachableDebit(t *testing.T) {
	original := map[int64]int64{11: 100}
	for _, tc := range []struct {
		name     string
		recovery map[int64]string
		want     []recipientDelta
		pending  bool
	}{
		{"transferred", map[int64]string{11: "recover"}, []recipientDelta{{11, -100}, {22, 120}}, false},
		{"confirmed departure cannot contact", map[int64]string{11: "departed_unreachable"}, []recipientDelta{{22, 120}}, false},
		{"missing recovery status", nil, []recipientDelta{{22, 120}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, pending, err := recommendationDeltas(original, map[int64]int64{22: 120}, tc.recovery)
			if err != nil || !reflect.DeepEqual(got, tc.want) || pending != tc.pending {
				t.Fatalf("deltas=%+v pending=%v error=%v", got, pending, err)
			}
		})
	}
	got, pending, err := recommendationDeltas(map[int64]int64{11: 0, 22: 120}, map[int64]int64{33: 90}, map[int64]string{22: "recover"})
	if err != nil || pending || !reflect.DeepEqual(got, []recipientDelta{{22, -120}, {33, 90}}) {
		t.Fatalf("successive correction=%+v pending=%v error=%v", got, pending, err)
	}
}
