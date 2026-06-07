// strategies.go — the built-in reasoning strategies, registered with the
// SDK factory registry (Story E15) exactly like channel/provider drivers
// (E10): init() self-registration, resolved by name at Loop.Run time.
// Custom strategies follow the same pattern from their own packages.
package reasoning

import (
	"context"
	"fmt"
	"time"

	sdkreasoning "github.com/soulacy/soulacy/sdk/reasoning"
	"github.com/soulacy/soulacy/sdk/registry"
)

func init() {
	registry.MustRegisterReasoningStrategy(StrategyReAct, func(cfg map[string]any) (sdkreasoning.Strategy, error) {
		return reactStrategy{}, nil
	})
	registry.MustRegisterReasoningStrategy(StrategyPlanExecute, func(cfg map[string]any) (sdkreasoning.Strategy, error) {
		return planExecuteStrategy{}, nil
	})
}

// ─── ReAct ────────────────────────────────────────────────────────────────────

// reactStrategy runs interleaved think/act/observe cycles until the LLM
// reports IsDone, MaxSteps is exhausted, or the context expires — then
// reflects on whatever trace exists.
type reactStrategy struct{}

func (reactStrategy) Run(ctx context.Context, env Env, taskInput string) ([]Step, ReflectResponse) {
	var steps []Step

	for i := 0; i < env.Config.MaxSteps; i++ {
		if ctx.Err() != nil {
			break
		}

		stepCtx, cancel := context.WithTimeout(ctx, env.Config.StepTimeout)
		stepStart := time.Now()

		think, err := env.LLM.Think(stepCtx, ThinkRequest{
			TaskInput:    taskInput,
			StepHistory:  steps,
			SystemPrompt: env.Config.SystemPrompt,
			ToolNames:    env.Config.ToolNames,
		})
		if err != nil {
			cancel()
			break // LLM error — reflect on partial trace
		}

		if think.IsDone {
			cancel()
			resp, _ := env.LLM.Reflect(ctx, ReflectRequest{
				TaskInput:    taskInput,
				Steps:        steps,
				SystemPrompt: env.Config.SystemPrompt,
				OutputFormat: env.Config.OutputFormat,
			})
			if resp.Output == "" && think.FinalAnswer != "" {
				resp.Output = think.FinalAnswer
			}
			return steps, resp
		}

		// Execute the tool — failures are wrapped as observations, not panics.
		obs := env.Tools.Execute(stepCtx, think.Action)
		obs = boundObservation(obs)

		steps = append(steps, Step{
			ID:       fmt.Sprintf("step-%d", i+1),
			Thought:  think.Thought,
			Action:   think.Action,
			Obs:      obs,
			Duration: time.Since(stepStart),
		})
		cancel()
	}

	// MaxSteps exhausted or LLM errored — reflect on what we have.
	resp, _ := env.LLM.Reflect(ctx, ReflectRequest{
		TaskInput:    taskInput,
		Steps:        steps,
		SystemPrompt: env.Config.SystemPrompt,
		OutputFormat: env.Config.OutputFormat,
	})
	return steps, resp
}

// ─── Plan-Execute ─────────────────────────────────────────────────────────────

// planExecuteStrategy decomposes the task with one Plan() call, executes the
// planned steps in order with dependency gating, then reflects. Planning
// failure falls back to ReAct.
type planExecuteStrategy struct{}

func (planExecuteStrategy) Run(ctx context.Context, env Env, taskInput string) ([]Step, ReflectResponse) {
	plan, err := env.LLM.Plan(ctx, env.Config.SystemPrompt, taskInput, env.Config.MaxPlanSteps)
	if err != nil {
		// Planning failed — fall back to ReAct.
		return reactStrategy{}.Run(ctx, env, taskInput)
	}

	completedIDs := map[string]bool{}
	var steps []Step

	for _, ps := range plan.Steps {
		if ctx.Err() != nil {
			break
		}

		// Dependency ordering: skip steps whose dependencies haven't completed.
		// Because the plan is already ordered, an unmet dependency means an
		// upstream step failed — we skip the dependent step gracefully.
		depsOK := true
		for _, dep := range ps.DependsOn {
			if !completedIDs[dep] {
				depsOK = false
				break
			}
		}
		if !depsOK {
			steps = append(steps, Step{
				ID:      ps.ID,
				Thought: ps.Description,
				Obs: Observation{
					Content: fmt.Sprintf("skipped: dependency %v not completed", ps.DependsOn),
				},
			})
			continue
		}

		stepCtx, cancel := context.WithTimeout(ctx, env.Config.StepTimeout)
		stepStart := time.Now()

		call := ToolCall{
			Tool:  ps.Tool,
			Input: map[string]string{"task": ps.Description},
		}
		obs := env.Tools.Execute(stepCtx, call)
		obs = boundObservation(obs)

		steps = append(steps, Step{
			ID:       ps.ID,
			Thought:  ps.Description,
			Action:   call,
			Obs:      obs,
			Duration: time.Since(stepStart),
		})
		completedIDs[ps.ID] = true
		cancel()
	}

	resp, _ := env.LLM.Reflect(ctx, ReflectRequest{
		TaskInput:    taskInput,
		Steps:        steps,
		SystemPrompt: env.Config.SystemPrompt,
		OutputFormat: env.Config.OutputFormat,
	})
	return steps, resp
}
