package core

import "github.com/phillipgreenii/pg-router/internal/eventqueue"

// The glue between the level projection and the durable log
// (eventqueue.Queue.AppendLevel / ReplayLevels).
//
// Exported names: QueueLevelAppender, ReplayLevels.

// QueueLevelAppender implements LevelAppender over the queue's durable level
// path: the append bypasses the admission limit and the unwritable mark, and
// returns the receipt sequence (the log offset of the level_set record).
type QueueLevelAppender struct{ Q *eventqueue.Queue }

// Append implements LevelAppender.
func (a QueueLevelAppender) Append(rec LevelRecord) (int64, error) {
	return a.Q.AppendLevel(toQueueLevel(rec))
}

// ReplayLevels feeds the level records the queue's startup replay found into the
// projection, in log order with their stored offsets. A lapsed operator override
// is dropped by ApplyRecord. It returns how many records it replayed.
func ReplayLevels(q *eventqueue.Queue, p *LevelProjection) int {
	return q.ReplayLevels(func(rec eventqueue.LevelRecord, off int64) {
		p.ApplyRecord(fromQueueLevel(rec), off)
	})
}

func toQueueLevel(r LevelRecord) eventqueue.LevelRecord {
	out := eventqueue.LevelRecord{
		Cleared: r.Cleared, Resource: r.Resource, Level: r.Level, Owner: r.Owner,
		ObservedAt: r.ObservedAt, ReceivedAt: r.ReceivedAt, ExpiresAt: r.ExpiresAt, ValidForS: r.ValidForS,
	}
	if r.Reading != nil {
		out.Reading = &eventqueue.LevelReading{Value: r.Reading.Value, Unit: r.Reading.Unit}
	}
	return out
}

func fromQueueLevel(r eventqueue.LevelRecord) LevelRecord {
	out := LevelRecord{
		Cleared: r.Cleared, Resource: r.Resource, Level: r.Level, Owner: r.Owner,
		ObservedAt: r.ObservedAt, ReceivedAt: r.ReceivedAt, ExpiresAt: r.ExpiresAt, ValidForS: r.ValidForS,
	}
	if r.Reading != nil {
		out.Reading = &LevelReading{Value: r.Reading.Value, Unit: r.Reading.Unit}
	}
	return out
}
