package polywire_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestProtocolInspectionCacheMatchesDirectConversion(t *testing.T) {
	ctx := context.Background()
	for _, typ := range outbound.Types() {
		fixture := readProtocolContract(t, typ)
		if fixture.StreamUnsupportedReason != "" {
			continue
		}
		cases := map[string][]contractEvent{"lifecycle": fixture.Stream}
		for name, semantic := range fixture.Semantics {
			if semantic.UnsupportedReason == "" {
				cases[name] = semantic.Events
			}
		}
		for name, frames := range cases {
			t.Run(typ.String()+"/"+name, func(t *testing.T) {
				cached, direct := outbound.Get(typ), outbound.Get(typ)
				inspector, ok := cached.(model.SourceEventInspector)
				if !ok {
					t.Fatal("streaming adapter has no stateless source inspector")
				}
				var sources []model.SourceEvent
				var snapshots [][]byte
				// Preview the entire sequence before conversion, including tool and
				// terminal frames. This must not consume any provider lifecycle state.
				for index, frame := range frames {
					source := model.SourceEvent{Type: frame.Type, Data: []byte(frame.Data), ID: "wire-id", Sequence: int64(index + 1), Transport: model.SourceTransportSSE}
					inspected, _, err := inspector.InspectSourceEvent(ctx, source)
					if err != nil {
						t.Fatal(err)
					}
					again, _, err := inspector.InspectSourceEvent(ctx, inspected)
					if err != nil || again.Decoded != inspected.Decoded {
						t.Fatalf("repeated inspection discarded the parsed DTO: %v", err)
					}
					snapshot, err := json.Marshal(inspected.Decoded)
					if err != nil {
						t.Fatal(err)
					}
					sources = append(sources, inspected)
					snapshots = append(snapshots, snapshot)
				}
				for index, source := range sources {
					got, err := cached.TransformSourceEvent(ctx, source)
					if err != nil {
						t.Fatal(err)
					}
					source.Decoded = nil
					want, err := direct.TransformSourceEvent(ctx, source)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("event %d changed after preview: got %+v, want %+v, err %v", index, got, want, err)
					}
				}
				for index, source := range sources {
					snapshot, err := json.Marshal(source.Decoded)
					if err != nil || string(snapshot) != string(snapshots[index]) {
						t.Fatalf("conversion mutated cached event %d: %v", index, err)
					}
				}
			})
		}
	}
}
