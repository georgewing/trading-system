package risk

import (
	"testing"
	"time"

	"trading-system/internal/hft/engine"
)

func TestMonitorMaxOpenOrders(t *testing.T) {
	m := NewMonitor(IntradayConfig{MaxOpenOrders: 2}, nil)
	now := time.Unix(1_700_000_000, 0)

	m.OnOrderAccepted(Subject{AccountID: 7}, now)
	m.OnOrderAccepted(Subject{AccountID: 7}, now)

	decision, _ := m.Check(Subject{AccountID: 7}, now)
	if decision.Approved || decision.Code != CodeMaxOpenOrders {
		t.Fatalf("decision = %+v, want %s", decision, CodeMaxOpenOrders)
	}

	m.OnOrderClosed(Subject{AccountID: 7})
	if decision, _ := m.Check(Subject{AccountID: 7}, now); !decision.Approved {
		t.Fatalf("after closing one order: %+v", decision)
	}
}

func TestMonitorCancelRatioPausesAndResumes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewMonitor(IntradayConfig{
		MaxCancelRatioBps: 5000,
		CancelWindow:      time.Minute,
		MinCancelSamples:  2,
		Cooldown:          10 * time.Second,
		ResumeAuto:        true,
	}, nil)

	m.OnOrderAccepted(Subject{AccountID: 7}, now)
	m.OnCancel(Subject{AccountID: 7}, now) // ratio 5000 bps, not strictly above the limit

	if decision, _ := m.Check(Subject{AccountID: 7}, now); !decision.Approved {
		t.Fatalf("ratio at the limit should pass: %+v", decision)
	}

	m.OnCancel(Subject{AccountID: 7}, now) // ratio 6666 bps

	decision, triggers := m.Check(Subject{AccountID: 7}, now)
	if decision.Approved || decision.Code != CodeStrategyPaused {
		t.Fatalf("decision = %+v, want paused", decision)
	}
	if len(triggers) != 1 || triggers[0].Code != CodeCancelRatio || triggers[0].Action != ActionPause {
		t.Fatalf("triggers = %+v", triggers)
	}

	if decision, _ := m.Check(Subject{AccountID: 7}, now.Add(5*time.Second)); decision.Code != CodeStrategyPaused {
		t.Fatalf("during cooldown decision = %+v", decision)
	}

	decision, triggers = m.Check(Subject{AccountID: 7}, now.Add(11*time.Second))
	if !decision.Approved {
		t.Fatalf("after cooldown decision = %+v", decision)
	}
	if len(triggers) != 1 || triggers[0].Code != CodeResumed {
		t.Fatalf("resume triggers = %+v", triggers)
	}

	action, _ := m.State(Subject{AccountID: 7})
	if action != ActionAllow {
		t.Fatalf("state = %s, want allow", action)
	}
}

func TestMonitorDailyLossHalts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pos := NewPositions()
	m := NewMonitor(IntradayConfig{MaxDailyLoss: 50}, pos)

	pos.ApplyFill(7, "X", engine.SideBuy, 100, 10)
	pos.ApplyFill(7, "X", engine.SideSell, 90, 10)

	triggers := m.Evaluate(Subject{AccountID: 7}, now)
	if len(triggers) != 1 || triggers[0].Code != CodeDailyLoss || triggers[0].Action != ActionHalt {
		t.Fatalf("triggers = %+v", triggers)
	}

	decision, _ := m.Check(Subject{AccountID: 7}, now)
	if decision.Approved || decision.Code != CodeStrategyHalted {
		t.Fatalf("decision = %+v, want halted", decision)
	}

	m.Reset(Subject{AccountID: 7})
	if decision, _ := m.Check(Subject{AccountID: 7}, now); !decision.Approved {
		t.Fatalf("after reset: %+v", decision)
	}
}

func TestMonitorDrawdownEscalatesByConfig(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	tests := []struct {
		name       string
		resumeAuto bool
		wantAction Action
	}{
		{"pause with auto resume", true, ActionPause},
		{"halt without auto resume", false, ActionHalt},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pos := NewPositions()
			m := NewMonitor(IntradayConfig{
				MaxDrawdownBps: 2000,
				Cooldown:       time.Minute,
				ResumeAuto:     tc.resumeAuto,
			}, pos)

			pos.ApplyFill(7, "X", engine.SideBuy, 100, 10)
			m.Evaluate(Subject{AccountID: 7}, now)

			pos.ApplyFill(7, "X", engine.SideSell, 120, 5)
			m.Evaluate(Subject{AccountID: 7}, now)

			pos.ApplyFill(7, "X", engine.SideSell, 80, 5)
			triggers := m.Evaluate(Subject{AccountID: 7}, now)

			if len(triggers) != 1 || triggers[0].Code != CodeDrawdown {
				t.Fatalf("triggers = %+v", triggers)
			}
			if triggers[0].Action != tc.wantAction {
				t.Fatalf("action = %s, want %s", triggers[0].Action, tc.wantAction)
			}
		})
	}
}

func TestMonitorDisabledIsPermissive(t *testing.T) {
	m := NewMonitor(IntradayConfig{}, nil)
	now := time.Unix(1_700_000_000, 0)

	if decision, triggers := m.Check(Subject{AccountID: 7}, now); !decision.Approved || len(triggers) != 0 {
		t.Fatalf("disabled monitor decision = %+v triggers = %+v", decision, triggers)
	}
	if triggers := m.Evaluate(Subject{AccountID: 7}, now); triggers != nil {
		t.Fatalf("disabled monitor triggers = %+v", triggers)
	}
}
