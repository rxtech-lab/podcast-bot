package config

import (
	"os"
	"strings"
)

const (
	defaultPointsSalePoints   = 1000.0
	defaultPointsSaleUSD      = 1.50
	defaultPointsCostLeverage = 3.0
)

func loadPointsCostConfig() (leverage float64, pointsPerUSD float64) {
	leverage = parseFloatEnvDefault("POINTS_COST_LEVERAGE", defaultPointsCostLeverage)
	pointsPerUSD = pointsPerUSDCostForLeverage(leverage)

	// Keep the existing raw-rate override for operators who need exact control.
	if strings.TrimSpace(os.Getenv("POINTS_PER_USD_COST")) != "" {
		pointsPerUSD = parseFloatEnvDefault("POINTS_PER_USD_COST", pointsPerUSD)
	}
	return leverage, pointsPerUSD
}

func pointsPerUSDCostForLeverage(leverage float64) float64 {
	if leverage <= 0 {
		return 0
	}
	return (defaultPointsSalePoints / defaultPointsSaleUSD) * leverage
}
