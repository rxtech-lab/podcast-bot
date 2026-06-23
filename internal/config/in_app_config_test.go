package config

import (
	"math"
	"testing"
)

func TestLoadPointsCostConfigDefaultsToTripleLeverage(t *testing.T) {
	t.Setenv("POINTS_COST_LEVERAGE", "")
	t.Setenv("POINTS_PER_USD_COST", "")

	leverage, pointsPerUSD := loadPointsCostConfig()
	if leverage != 3 {
		t.Fatalf("leverage = %.2f, want 3", leverage)
	}
	if pointsPerUSD != 2000 {
		t.Fatalf("pointsPerUSD = %.2f, want 2000", pointsPerUSD)
	}
}

func TestLoadPointsCostConfigUsesLeverageOverride(t *testing.T) {
	t.Setenv("POINTS_COST_LEVERAGE", "2")
	t.Setenv("POINTS_PER_USD_COST", "")

	leverage, pointsPerUSD := loadPointsCostConfig()
	if leverage != 2 {
		t.Fatalf("leverage = %.2f, want 2", leverage)
	}
	if math.Abs(pointsPerUSD-1333.3333333333333) > 0.0000001 {
		t.Fatalf("pointsPerUSD = %.13f, want 1333.3333333333333", pointsPerUSD)
	}
}

func TestLoadPointsCostConfigKeepsRawRateOverride(t *testing.T) {
	t.Setenv("POINTS_COST_LEVERAGE", "3")
	t.Setenv("POINTS_PER_USD_COST", "1340")

	leverage, pointsPerUSD := loadPointsCostConfig()
	if leverage != 3 {
		t.Fatalf("leverage = %.2f, want 3", leverage)
	}
	if pointsPerUSD != 1340 {
		t.Fatalf("pointsPerUSD = %.2f, want 1340", pointsPerUSD)
	}
}
