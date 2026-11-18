package pipeline

import (
	"context"
	"errors"
	"fmt"

	"trading-system/internal/engine"
	"trading-system/internal/risk"
)

// RiskGuard is the narrow pre-trade risk interface consumed by EventBus. It is
// implemented by *risk.Guard; tests may provide a fake.
type RiskGuard interface {
	Check(ctx context.Context, req risk.Request) risk.Decision
}

// Results reported to observability.Recorder.IncRiskDecision.
const (
	riskResultApproved = "approved"
	riskResultRejected = "rejected"
)

// SetRiskGuard installs the optional pre-trade risk stage. When guard is nil
// the stage is disabled and orders go straight to the matcher, which is the
// default behavior. quantityScale is the multiplier used by
// engine.ParseQuantity to decode Order.Quantity into integer lots.
//
// Call it before Start; the guard is read on the single processing goroutine.
func (b *EventBus) SetRiskGuard(guard RiskGuard, quantityScale int64) {
	if quantityScale <= 0 {
		quantityScale = 1
	}
	b.riskGuard = guard
	b.riskQuantityScale = quantityScale
}

// checkRisk runs the pre-trade risk stage. It returns ("", true) when the order
// may proceed to the matcher, or (reason, false) when it must be rejected
// without touching the order book.
func (b *EventBus) checkRisk(ctx context.Context, order engine.Order) (string, bool) {
	guard := b.riskGuard
	if guard == nil {
		return "", true
	}

	qty, err := engine.ParseQuantity(order.Quantity, b.riskQuantityScale)
	if err != nil {
		b.metrics.IncRiskDecision(risk.CodeInvalidRequest, riskResultRejected)
		return fmt.Sprintf("%s: invalid order quantity %q: %v", risk.CodeInvalidRequest, order.Quantity, err), false
	}

	decision := guard.Check(ctx, risk.Request{
		AccountID:      order.AccountID,
		ClientOrderID:  order.ClientOrderID,
		Symbol:         order.Symbol,
		Side:           order.Side,
		Type:           order.Type,
		TIF:            order.TIF,
		Price:          order.Price,
		Qty:            qty,
		ReferencePrice: b.matcher.ReferencePrice(),
	})

	if !decision.Approved {
		b.metrics.IncRiskDecision(decision.Code, riskResultRejected)
		return rejectionReason(decision), false
	}

	b.metrics.IncRiskDecision(decision.Code, riskResultApproved)
	return "", true
}

// rejectionReason renders a risk decision as the ExecReport reason, always
// starting with the decision code so callers can match on it.
func rejectionReason(decision risk.Decision) string {
	code := decision.Code
	if code == "" {
		code = risk.CodeInvalidRequest
	}
	if decision.Message == "" {
		return code
	}
	return code + ": " + decision.Message
}

// rejectCommand emits the same rejected ExecReport shape used for matcher
// errors, without invoking the matcher.
func (b *EventBus) rejectCommand(ctx context.Context, cmd OrderCommand, reason string) {
	rejectErr := errors.New(reason)

	b.sendResult(ctx, cmd.ResultCh, MatchResult{Err: rejectErr})

	for _, report := range buildExecReports(cmd.Order, engine.SubmitResult{}, rejectErr) {
		if err := b.enqueueReport(ctx, report); err != nil {
			logEnqueueFailure(report, err)
		}
	}
}
