package communication

import "context"

type Runner interface {
	Run(context.Context, Job, User) (string, error)
}

type outcomeRunner interface {
	RunOutcome(context.Context, Job) (RunOutcome, error)
}
