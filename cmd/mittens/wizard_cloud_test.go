package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func TestCloudSelectionItemsKeepsUnavailableSavedSelections(t *testing.T) {
	items, preserve := cloudSelectionItems([]registry.ListItem{
		{Label: "Development", Value: "dev"},
	}, []string{"dev", "production"}, nil)

	if preserve {
		t.Fatal("successful discovery should not preserve the whole existing config")
	}
	want := []registry.ListItem{
		{Label: "Development", Value: "dev"},
		{Label: "production — unavailable locally (saved selection)", Value: "production"},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("items = %#v, want %#v", items, want)
	}
}

func TestCloudSelectionItemsDistinguishesErrorFromEmptyDiscovery(t *testing.T) {
	t.Run("error keeps existing config", func(t *testing.T) {
		items, preserve := cloudSelectionItems(nil, []string{"production"}, errors.New("credential store unavailable"))
		if items != nil || !preserve {
			t.Fatalf("error result = (%#v, %t), want (nil, true)", items, preserve)
		}
		existing := []string{"--aws production"}
		if got := preserveCloudConfig(existing, "--aws"); !reflect.DeepEqual(got, existing) {
			t.Fatalf("preserved config = %#v, want %#v", got, existing)
		}
	})

	t.Run("empty discovery remains editable", func(t *testing.T) {
		items, preserve := cloudSelectionItems(nil, []string{"production"}, nil)
		want := []registry.ListItem{{Label: "production — unavailable locally (saved selection)", Value: "production"}}
		if preserve || !reflect.DeepEqual(items, want) {
			t.Fatalf("empty result = (%#v, %t), want (%#v, false)", items, preserve, want)
		}
	})

	t.Run("empty discovery without saved settings remains empty", func(t *testing.T) {
		items, preserve := cloudSelectionItems(nil, nil, nil)
		if items != nil || preserve {
			t.Fatalf("empty result = (%#v, %t), want (nil, false)", items, preserve)
		}
	})
}
