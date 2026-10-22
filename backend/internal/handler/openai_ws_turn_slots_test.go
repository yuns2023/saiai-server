package handler

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSTurnSlots_FastAndSlowPipelinedTurnsShareSlotsUntilLastFinishes(t *testing.T) {
	slots := &openAIWSTurnSlots{}
	userReleased, accountReleased := 0, 0
	slots.setUser(func() { userReleased++ })
	slots.setAccount(func() { accountReleased++ })
	acquireCalls := 0
	acquire := func() (func(), func(), error) {
		acquireCalls++
		return func() { userReleased++ }, func() { accountReleased++ }, nil
	}

	require.NoError(t, slots.beginTurn(1, acquire)) // slow turn
	require.NoError(t, slots.beginTurn(2, acquire)) // fast turn, pipelined
	require.NoError(t, slots.beginTurn(1, acquire)) // failover replay of turn 1
	require.Zero(t, acquireCalls, "the held session slots are shared")

	slots.endTurn(2) // fast turn completes first
	require.Zero(t, userReleased, "slots stay held while the slow turn is outstanding")
	require.Zero(t, accountReleased)

	slots.endTurn(1) // slow turn (for example after a post-disconnect drain)
	require.Equal(t, 1, userReleased)
	require.Equal(t, 1, accountReleased)

	// An idle session re-acquires for its next turn and releases after it.
	require.NoError(t, slots.beginTurn(3, acquire))
	require.Equal(t, 1, acquireCalls)
	slots.endTurn(3)
	require.Equal(t, 2, userReleased)
	require.Equal(t, 2, accountReleased)

	// A surplus completion report does not double release.
	slots.endTurn(4)
	require.Equal(t, 2, userReleased)
}

func TestOpenAIWSTurnSlots_FailedAcquireDoesNotCountTurn(t *testing.T) {
	slots := &openAIWSTurnSlots{}
	err := slots.beginTurn(1, func() (func(), func(), error) { return nil, nil, errors.New("busy") })
	require.Error(t, err)
	require.Empty(t, slots.inFlight)
	released := 0
	require.NoError(t, slots.beginTurn(2, func() (func(), func(), error) {
		return func() { released++ }, func() { released++ }, nil
	}))
	slots.release()
	require.Equal(t, 2, released)
}
