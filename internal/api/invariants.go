package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/async-wizard/paisa-wallet/internal/wallet"
	"github.com/prometheus/client_golang/prometheus"
)

// invariants is a live audit of conservation and no-overdraft. It returns 200 when every
// invariant holds and 500 when one is broken, so an uptime check can alert on it.
func (h *handler) invariants(w http.ResponseWriter, r *http.Request) {
	inv, err := h.svc.CheckInvariants(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	status := http.StatusOK
	if !inv.OK {
		status = http.StatusInternalServerError
		slog.ErrorContext(r.Context(), "invariant.violated", "event", "invariant.violated", "invariants", inv)
	}
	writeJSON(w, status, inv)
}

// InvariantsCollector exposes the invariant audit as Prometheus gauges. /metrics is public,
// so results are cached briefly to keep repeated scrapes from repeating full table scans.
type InvariantsCollector struct {
	svc *wallet.Service

	mu      sync.Mutex
	last    wallet.Invariants
	fetched time.Time
}

func NewInvariantsCollector(svc *wallet.Service) *InvariantsCollector {
	return &InvariantsCollector{svc: svc}
}

var (
	ledgerImbalanceDesc = prometheus.NewDesc("paisa_ledger_imbalance_paise",
		"Sum of all ledger entries. Must be 0.", nil, nil)
	balanceSumDesc = prometheus.NewDesc("paisa_wallet_balance_sum_paise",
		"Sum of all wallet balances including the treasury. Must be 0.", nil, nil)
	userBalanceSumDesc = prometheus.NewDesc("paisa_user_balance_sum_paise",
		"Sum of user wallet balances (total money in circulation).", nil, nil)
	negativeWalletsDesc = prometheus.NewDesc("paisa_negative_user_wallets",
		"User wallets with a negative balance. Must be 0.", nil, nil)
	unreconciledDesc = prometheus.NewDesc("paisa_unreconciled_wallets",
		"Wallets whose balance differs from the sum of their ledger entries. Must be 0.", nil, nil)
	invariantsOKDesc = prometheus.NewDesc("paisa_invariants_ok",
		"1 if every invariant holds, 0 otherwise.", nil, nil)
)

func (c *InvariantsCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{ledgerImbalanceDesc, balanceSumDesc, userBalanceSumDesc,
		negativeWalletsDesc, unreconciledDesc, invariantsOKDesc} {
		ch <- d
	}
}

func (c *InvariantsCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if time.Since(c.fetched) > 5*time.Second {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		inv, err := c.svc.CheckInvariants(ctx)
		cancel()
		if err != nil {
			slog.Error("invariant check failed", "err", err.Error())
			return
		}
		c.last, c.fetched = inv, time.Now()
	}

	ok := 0.0
	if c.last.OK {
		ok = 1
	}
	gauge := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v)
	}
	gauge(ledgerImbalanceDesc, float64(c.last.LedgerSumPaise))
	gauge(balanceSumDesc, float64(c.last.BalanceSumPaise))
	gauge(userBalanceSumDesc, float64(c.last.UserBalanceSumPaise))
	gauge(negativeWalletsDesc, float64(c.last.NegativeUserWallets))
	gauge(unreconciledDesc, float64(c.last.UnreconciledWallets))
	gauge(invariantsOKDesc, ok)
}
