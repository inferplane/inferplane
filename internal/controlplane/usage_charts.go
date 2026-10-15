package controlplane

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/inferplane/inferplane/internal/telemetry"
)

type chartDay struct {
	Day   string `json:"day"`
	Spend int64  `json:"spent_micro_usd"`
}

type chartModel struct {
	Key   string `json:"key"`
	Spend int64  `json:"spent_micro_usd"`
}

// Charts roll up retained reported windows, never financial admission credit.
// The bounded response avoids buffering/exporting all raw windows in a browser.
// A failed row cursor produces no successful partial chart or memory fallback.
func (s *UsageServer) handleCharts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("since") == "" || q.Get("until") == "" {
		writeJSONError(w, http.StatusBadRequest, "since and until are required")
		return
	}
	since, err := parseTimeParam(q.Get("since"), time.Time{})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad since")
		return
	}
	until, err := parseTimeParam(q.Get("until"), time.Time{})
	if err != nil || !until.After(since) || until.Sub(since) > 366*24*time.Hour {
		writeJSONError(w, http.StatusBadRequest, "select a positive range of at most 366 days")
		return
	}
	days, models := map[string]int64{}, map[string]int64{}
	var total int64
	err = s.agg.Rows(r.Context(), since, until, func(row telemetry.StoredRow) error {
		day := row.WindowStart.UTC().Format("2006-01-02")
		days[day] = addAccounting(days[day], row.SpentMicroUSD)
		models[row.Model] = addAccounting(models[row.Model], row.SpentMicroUSD)
		total = addAccounting(total, row.SpentMicroUSD)
		return nil
	})
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "chart history unavailable")
		return
	}
	dayRows := make([]chartDay, 0, len(days))
	for day, spend := range days {
		dayRows = append(dayRows, chartDay{Day: day, Spend: spend})
	}
	sort.Slice(dayRows, func(i, j int) bool { return dayRows[i].Day < dayRows[j].Day })
	modelRows := make([]chartModel, 0, len(models))
	for name, spend := range models {
		modelRows = append(modelRows, chartModel{Key: name, Spend: spend})
	}
	sort.Slice(modelRows, func(i, j int) bool {
		if modelRows[i].Spend == modelRows[j].Spend {
			return modelRows[i].Key < modelRows[j].Key
		}
		return modelRows[i].Spend > modelRows[j].Spend
	})
	// Keep top names explicit; do not synthesize an "Other" model that could
	// collide with a real configured name.
	var other int64
	if len(modelRows) > 8 {
		for _, row := range modelRows[8:] {
			other = addAccounting(other, row.Spend)
		}
		modelRows = modelRows[:8]
	}
	body := struct {
		Total      int64        `json:"total_micro_usd"`
		Days       []chartDay   `json:"days"`
		Models     []chartModel `json:"models"`
		OtherSpend int64        `json:"other_micro_usd"`
		ModelCount int          `json:"model_count"`
	}{total, dayRows, modelRows, other, len(models)}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}
