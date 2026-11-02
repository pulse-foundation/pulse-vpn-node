package traffic

import "time"

const liveMaxAge = 5 * PollInterval

type liveObservation struct {
	epoch      string
	sequence   int64
	observedAt time.Time
	users      map[string]Live
	idleRates  bool
}

// Rates use only consecutive, committed observations. Cumulative bytes from an
// older newly visible connection belong to an unknown window, not this interval.
func observeLive(current, previous *CommittedObservation) *liveObservation {
	live := &liveObservation{epoch: current.AccountingEpoch, sequence: current.AccountingSequence, observedAt: current.ObservedAt, users: map[string]Live{}}
	elapsed := time.Duration(0)
	if previous != nil && current.Generation == previous.Generation && current.AccountingEpoch == previous.AccountingEpoch && current.AccountingSequence == previous.AccountingSequence+1 {
		elapsed = current.ObservedAt.Sub(previous.ObservedAt)
	}
	live.idleRates = elapsed > 0 && elapsed <= liveMaxAge
	old := map[string]Connection{}
	if live.idleRates {
		for _, connection := range previous.Connections {
			if connection.User != "" {
				old[connection.ID] = connection
			}
		}
	}
	invalid := map[string]bool{}
	previousUsers := map[string]bool{}
	if previous != nil {
		for _, user := range previous.Users {
			previousUsers[user] = true
		}
		for _, user := range previous.UnratedUsers {
			invalid[user] = true
		}
	}
	for _, name := range current.Users {
		live.users[name] = live.emptyUser()
		if !previousUsers[name] {
			invalid[name] = true
		}
	}
	for _, user := range current.UnratedUsers {
		invalid[user] = true
	}
	for _, connection := range current.Connections {
		if connection.User == "" {
			continue
		}
		user, ok := live.users[connection.User]
		if !ok {
			user = live.emptyUser()
		}
		user.ActiveConnections++
		if live.idleRates {
			up, down := connection.Upload, connection.Download
			prior, exists := old[connection.ID]
			if exists {
				delete(old, connection.ID)
				if prior.User != connection.User || !prior.StartedAt.Equal(connection.StartedAt) || up < prior.Upload || down < prior.Download {
					invalid[connection.User] = true
					invalid[prior.User] = true
				} else {
					up -= prior.Upload
					down -= prior.Download
				}
			} else if !connection.StartedAt.After(previous.ObservedAt) {
				invalid[connection.User] = true
			}
			*user.UploadBytesPerSecond += float64(up) / elapsed.Seconds()
			*user.DownloadBytesPerSecond += float64(down) / elapsed.Seconds()
		}
		live.users[connection.User] = user
	}
	// A vanished connection may have unobserved final bytes. Wait for another
	// complete idle observation before reporting an observed zero rate.
	for _, connection := range old {
		invalid[connection.User] = true
	}
	for name := range invalid {
		user, ok := live.users[name]
		if !ok {
			user = live.emptyUser()
		}
		user.UploadBytesPerSecond = nil
		user.DownloadBytesPerSecond = nil
		live.users[name] = user
	}
	return live
}

func (l *liveObservation) emptyUser() Live {
	user := Live{ObservedAt: l.observedAt}
	if l.idleRates {
		user.UploadBytesPerSecond = new(float64)
		user.DownloadBytesPerSecond = new(float64)
	}
	return user
}

func cloneLive(live *Live) *Live {
	if live == nil {
		return nil
	}
	copy := *live
	if live.UploadBytesPerSecond != nil {
		value := *live.UploadBytesPerSecond
		copy.UploadBytesPerSecond = &value
	}
	if live.DownloadBytesPerSecond != nil {
		value := *live.DownloadBytesPerSecond
		copy.DownloadBytesPerSecond = &value
	}
	return &copy
}

func cloneTotals(totals Totals) Totals {
	if totals.ObservedAt != nil {
		value := *totals.ObservedAt
		totals.ObservedAt = &value
	}
	totals.Users = append([]UserTotal{}, totals.Users...)
	for i := range totals.Users {
		if activity := totals.Users[i].LastActivityAt; activity != nil {
			value := *activity
			totals.Users[i].LastActivityAt = &value
		}
		totals.Users[i].Live = cloneLive(totals.Users[i].Live)
	}
	return totals
}

func observationStale(observed *time.Time) bool {
	if observed == nil {
		return true
	}
	age := time.Since(*observed)
	return age < 0 || age > liveMaxAge
}
