//go:build integration

package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type accountSortFixture struct {
	name           string
	status         string
	schedulable    bool
	priority       int
	rateMultiplier float64
	lastUsedAt     *time.Time
	expiresAt      *time.Time
}

// TestAccountRepository_ListWithFilters_Sort checks every whitelisted sort
// key in both directions against an independent in-test oracle: primary key,
// then id in the same direction, NULL timestamps last in both directions.
func TestAccountRepository_ListWithFilters_Sort(t *testing.T) {
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	ctx := context.Background()

	// A unique search token isolates these rows from accounts committed by
	// other tests in the shared database.
	token := fmt.Sprintf("acctsort%d", time.Now().UnixNano())
	base := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	at := func(h int) *time.Time { v := base.Add(time.Duration(h) * time.Hour); return &v }

	// Deliberate ties on every key (two "alpha" names, equal priorities,
	// equal timestamps, several NULLs) so the id tiebreaker is exercised.
	fixtures := []accountSortFixture{
		{name: "Bravo", status: service.StatusActive, schedulable: true, priority: 10, rateMultiplier: 1, lastUsedAt: at(3), expiresAt: nil},
		{name: "alpha", status: service.StatusDisabled, schedulable: false, priority: 50, rateMultiplier: 0.5, lastUsedAt: nil, expiresAt: at(48)},
		{name: "Charlie", status: service.StatusError, schedulable: true, priority: 10, rateMultiplier: 2.25, lastUsedAt: at(1), expiresAt: at(24)},
		{name: "alpha", status: service.StatusActive, schedulable: false, priority: 1, rateMultiplier: 1, lastUsedAt: at(3), expiresAt: nil},
		{name: "delta", status: service.StatusDisabled, schedulable: true, priority: 50, rateMultiplier: 0.5, lastUsedAt: nil, expiresAt: at(24)},
		{name: "Echo", status: service.StatusActive, schedulable: true, priority: 10, rateMultiplier: 1, lastUsedAt: at(2), expiresAt: nil},
	}

	type row struct {
		id int64
		f  accountSortFixture
	}
	rows := make([]row, 0, len(fixtures))
	for _, f := range fixtures {
		acc := mustCreateAccount(t, client, &service.Account{Name: token + "-" + f.name, Status: f.status})
		upd := client.Account.UpdateOneID(acc.ID).
			SetSchedulable(f.schedulable).
			SetPriority(f.priority).
			SetRateMultiplier(f.rateMultiplier)
		if f.lastUsedAt != nil {
			upd.SetLastUsedAt(*f.lastUsedAt)
		} else {
			upd.ClearLastUsedAt()
		}
		if f.expiresAt != nil {
			upd.SetExpiresAt(*f.expiresAt)
		} else {
			upd.ClearExpiresAt()
		}
		require.NoError(t, upd.Exec(ctx))
		rows = append(rows, row{id: acc.ID, f: f})
	}

	cmpInt := func(a, b int64) int {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
		return 0
	}
	cmpTime := func(a, b *time.Time) (c int, nullDecides bool, aFirst bool) {
		switch {
		case a == nil && b == nil:
			return 0, false, false
		case a == nil:
			return 0, true, false
		case b == nil:
			return 0, true, true
		}
		return a.Compare(*b), false, false
	}
	b2i := func(v bool) int64 {
		if v {
			return 1
		}
		return 0
	}
	expected := func(by string, desc bool) []int64 {
		out := append([]row(nil), rows...)
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i], out[j]
			var c int
			switch by {
			case service.AccountSortByID:
				c = 0
			case service.AccountSortByName:
				c = strings.Compare(strings.ToLower(a.f.name), strings.ToLower(b.f.name))
			case service.AccountSortByStatus:
				c = strings.Compare(a.f.status, b.f.status)
			case service.AccountSortBySchedulable:
				c = cmpInt(b2i(a.f.schedulable), b2i(b.f.schedulable))
			case service.AccountSortByPriority:
				c = cmpInt(int64(a.f.priority), int64(b.f.priority))
			case service.AccountSortByRateMultiplier:
				c = cmpInt(int64(a.f.rateMultiplier*10000), int64(b.f.rateMultiplier*10000))
			case service.AccountSortByLastUsedAt, service.AccountSortByExpiresAt:
				ta, tb := a.f.lastUsedAt, b.f.lastUsedAt
				if by == service.AccountSortByExpiresAt {
					ta, tb = a.f.expiresAt, b.f.expiresAt
				}
				var nullDecides, aFirst bool
				c, nullDecides, aFirst = cmpTime(ta, tb)
				if nullDecides {
					return aFirst // NULLs last regardless of direction
				}
			default:
				t.Fatalf("unexpected sort key %q", by)
			}
			if c == 0 {
				c = cmpInt(a.id, b.id)
			}
			if desc {
				return c > 0
			}
			return c < 0
		})
		ids := make([]int64, len(out))
		for i := range out {
			ids[i] = out[i].id
		}
		return ids
	}

	list := func(page, size int, s service.AccountListSort) []int64 {
		accounts, res, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: page, PageSize: size}, "", "", "", token, 0, s)
		require.NoError(t, err)
		require.Equal(t, int64(len(rows)), res.Total)
		ids := make([]int64, len(accounts))
		for i := range accounts {
			ids[i] = accounts[i].ID
		}
		return ids
	}

	keys := []string{
		service.AccountSortByID, service.AccountSortByName, service.AccountSortByStatus,
		service.AccountSortBySchedulable, service.AccountSortByPriority, service.AccountSortByRateMultiplier,
		service.AccountSortByLastUsedAt, service.AccountSortByExpiresAt,
	}
	for _, key := range keys {
		for _, desc := range []bool{false, true} {
			s := service.AccountListSort{By: key, Desc: desc}
			t.Run(fmt.Sprintf("%s_desc=%v", key, desc), func(t *testing.T) {
				want := expected(key, desc)
				require.Equal(t, want, list(1, 50, s), "single page")

				// Pages of 2 concatenate to exactly the full order: no
				// duplicates or skips across page boundaries.
				var paged []int64
				for p := 1; p <= 3; p++ {
					paged = append(paged, list(p, 2, s)...)
				}
				require.Equal(t, want, paged, "paged")
			})
		}
	}

	t.Run("nulls_last_both_directions", func(t *testing.T) {
		nullLast := func(ids []int64, isNull func(row) bool) {
			byID := map[int64]row{}
			for _, r := range rows {
				byID[r.id] = r
			}
			seenNull := false
			for _, id := range ids {
				if isNull(byID[id]) {
					seenNull = true
					continue
				}
				require.False(t, seenNull, "non-NULL row after a NULL row: %v", ids)
			}
		}
		for _, desc := range []bool{false, true} {
			nullLast(list(1, 50, service.AccountListSort{By: service.AccountSortByLastUsedAt, Desc: desc}), func(r row) bool { return r.f.lastUsedAt == nil })
			nullLast(list(1, 50, service.AccountListSort{By: service.AccountSortByExpiresAt, Desc: desc}), func(r row) bool { return r.f.expiresAt == nil })
		}
	})

	t.Run("default_and_unknown_fall_back_to_id_desc", func(t *testing.T) {
		want := expected(service.AccountSortByID, true)
		require.Equal(t, want, list(1, 50, service.AccountListSort{}))
		require.Equal(t, want, list(1, 50, service.NormalizeAccountListSort("bogus", "asc")))
		require.Equal(t, want, list(1, 50, service.NormalizeAccountListSort("", "asc")))
	})
}
