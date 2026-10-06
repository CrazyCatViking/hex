package hex

import (
	"encoding/json"
	"math"
	"testing"
)

func TestAIPriceIncludesCacheCategoriesAndExplicitFreeOverrides(t *testing.T) {
	usage := AIUsage{InputTokens: 10, CachedInputTokens: 100, CacheWriteTokens: 20, OutputTokens: 5}
	for _, test := range []struct {
		name string
		json string
		want int64
	}{
		{"legacy defaults", `{"input":2,"output":4}`, 110},
		{"cache rates", `{"input":2,"cachedInput":3,"cacheWrite":4,"output":4}`, 420},
		{"explicit free cache", `{"input":2,"output":4,"cachedInputOverride":0,"cacheWriteOverride":0}`, 40},
	} {
		t.Run(test.name, func(t *testing.T) {
			var price AIPrice
			if err := json.Unmarshal([]byte(test.json), &price); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(price)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &price); err != nil {
				t.Fatal(err)
			}
			if !price.valid() || price.costMicros(usage) != test.want {
				t.Fatalf("incorrect cache pricing: %+v, cost %d", price, price.costMicros(usage))
			}
		})
	}
}

func TestAIPriceRejectsInvalidCacheRates(t *testing.T) {
	negative := -1.0
	for _, price := range []AIPrice{
		{Input: -1}, {Output: math.Inf(1)}, {CachedInput: math.NaN()},
		{CacheWrite: -1}, {CachedInputOverride: &negative}, {CacheWriteOverride: &negative},
	} {
		if price.valid() {
			t.Fatalf("invalid price accepted: %+v", price)
		}
	}
}
