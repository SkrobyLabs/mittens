package main

const capabilitySchemaVersion = 1

func kitchenCapabilityMetadata() map[string]any {
	return map[string]any{
		"schemaVersion": capabilitySchemaVersion,
		"stability":     "beta",
		"compatibility": map[string]any{
			"additiveFields":        "allowed within a schema version",
			"breakingChange":        "requires schemaVersion increment",
			"unknownFields":         "ignore",
			"sectionVersioning":     true,
			"sectionVersionMeaning": "increments when that section adds or changes machine-readable semantics",
		},
		"sections": map[string]any{
			"cli": map[string]any{
				"version":   4,
				"stability": "beta",
			},
			"api": map[string]any{
				"version":   4,
				"stability": "beta",
			},
			"planning": map[string]any{
				"version":   1,
				"stability": "beta",
			},
			"git": map[string]any{
				"version":   1,
				"stability": "beta",
			},
			"snapshots": map[string]any{
				"version":   2,
				"stability": "beta",
			},
			"runtime": map[string]any{
				"version":   2,
				"stability": "beta",
			},
		},
	}
}

func kitchenCapabilities() map[string]any {
	return map[string]any{
		"meta": kitchenCapabilityMetadata(),
		"cli": map[string]any{
			"submit": map[string]any{
				"review": true,
				"inputs": []string{"inline", "file", "stdin", "editor"},
				"options": map[string]any{
					"lineage": map[string]any{
						"type": "string",
					},
					"auto": map[string]any{
						"type":    "bool",
						"default": false,
					},
					"review": map[string]any{
						"type":    "bool",
						"default": false,
					},
					"reviewRounds": map[string]any{
						"type":    "int",
						"minimum": 0,
						"default": 0,
					},
					"maxReviewRevisions": map[string]any{
						"type":           "int",
						"minimum":        -1,
						"default":        -1,
						"special":        map[string]any{"-1": "use default", "0": "disable automatic refinement"},
						"requiresReview": true,
					},
				},
			},
			"status": map[string]any{
				"historyLimitOverride": true,
				"options": map[string]any{
					"historyLimit": map[string]any{
						"type":    "int",
						"minimum": -1,
						"default": -1,
						"special": map[string]any{"-1": "use configured default", "0": "omit embedded history"},
					},
				},
			},
			"history": map[string]any{
				"cycleFilter": true,
				"json":        true,
				"options": map[string]any{
					"cycle": map[string]any{
						"type":    "int",
						"minimum": 0,
						"default": 0,
					},
					"json": map[string]any{
						"type":    "bool",
						"default": false,
					},
				},
			},
			"evidence": map[string]any{
				"defaultTier": "rich",
				"tiers":       []string{"compact", "rich"},
				"options": map[string]any{
					"compact": map[string]any{
						"type":    "bool",
						"default": false,
						"meaning": "emit the compact evidence tier instead of the default rich payload",
					},
				},
			},
			"config": map[string]any{
				"pathsOnly": true,
			},
			"capabilities": map[string]any{
				"cliOnly": true,
			},
			"merge": map[string]any{
				"squash":   true,
				"noCommit": true,
				"mode": map[string]any{
					"values":  []string{"direct", "squash"},
					"default": "direct",
				},
			},
			"retry": map[string]any{
				"target": "task",
				"options": map[string]any{
					"sameWorker": map[string]any{
						"type":    "bool",
						"default": false,
						"meaning": "retry on any eligible idle worker instead of requiring a fresh worker",
					},
				},
			},
			"delete": map[string]any{
				"target":  "plan",
				"effects": []string{"cancel_active_tasks", "remove_plan_storage", "remove_plan_tasks"},
			},
		},
		"api": map[string]any{
			"auth": map[string]any{
				"headerToken": true,
				"bearerToken": true,
			},
			"endpoints": map[string]any{
				"ideas":          "/v1/ideas",
				"plans":          "/v1/plans",
				"planDelete":     "/v1/plans/{id}/purge",
				"planHistory":    "/v1/plans/{id}/history",
				"planEvidence":   "/v1/plans/{id}/evidence",
				"taskRetry":      "/v1/tasks/{id}/retry",
				"questions":      "/v1/questions",
				"queue":          "/v1/queue",
				"workers":        "/v1/workers",
				"events":         "/v1/events",
				"lineages":       "/v1/lineages",
				"providerHealth": "/v1/providers/health",
				"meta":           "/v1/meta",
			},
			"events": map[string]any{
				"snapshot":             true,
				"progress":             true,
				"historyEntry":         true,
				"historyLimitOverride": true,
				"snapshotPolicy":       true,
				"query": map[string]any{
					"historyLimit": map[string]any{
						"type":    "int",
						"minimum": -1,
						"default": -1,
						"special": map[string]any{"-1": "use configured default", "0": "omit embedded history"},
					},
				},
			},
			"planEvidence": map[string]any{
				"defaultTier": "rich",
				"tiers":       []string{"compact", "rich"},
				"query": map[string]any{
					"tier": map[string]any{
						"type":    "string",
						"values":  []string{"compact", "rich"},
						"default": "rich",
					},
				},
			},
		},
		"planning": map[string]any{
			"workerDrivenPlanning":      true,
			"workerDrivenReview":        true,
			"automaticReviewRefinement": true,
			"plannerQuestions":          true,
			"historyPersistence":        true,
			"reviewStates":              []string{"planning", "reviewing", "pending_approval", "active", "planning_failed", "completed", "merged", "closed", "rejected"},
		},
		"git": map[string]any{
			"lineageMerge":      true,
			"mergeCheck":        true,
			"mergePreview":      true,
			"worktreeCleanup":   true,
			"lineageInspection": true,
		},
		"snapshots": map[string]any{
			"planHistoryWindowing":        true,
			"configuredHistoryLimit":      true,
			"overrideHistoryLimit":        true,
			"embeddedHistoryLimitDefault": defaultPlanProgressHistoryLimit,
		},
		"providers": []map[string]any{
			{
				"provider":    "anthropic",
				"adapter":     "claude-code",
				"models":      []string{"haiku", "sonnet", "opus"},
				"description": "Anthropic Claude via claude-code CLI",
			},
			{
				"provider":    "codex",
				"adapter":     "openai-codex",
				"models":      []string{"codex-mini", "o4-mini"},
				"description": "OpenAI Codex via codex CLI",
			},
			{
				"provider":    "gemini",
				"adapter":     "gemini-cli",
				"models":      []string{"gemini-2.0-flash", "gemini-2.5-pro"},
				"description": "Google Gemini via gemini-cli",
			},
		},
		"runtime": map[string]any{
			"transport":       "unix-socket",
			"eventForwarding": true,
			"workerActivity":  "heartbeat",
			"endpoints": map[string]any{
				"workers": true,
				"events":  true,
				"recycle": map[string]any{
					"available":       true,
					"implemented":     true,
					"status":          "implemented",
					"resetsSession":   true,
					"mechanism":       "broker_poll",
					"blocking":        false,
					"clearsArtifacts": true,
					"reason":          "clears runtime metadata and worker state files, then asks the worker to force-clean its adapter session on the next broker poll",
				},
				"assignments": map[string]any{
					"available":         true,
					"implemented":       true,
					"status":            "persist_only",
					"consumedByWorkers": false,
					"reason":            "accepts and persists assignment records for runtime inspection, but workers still execute through the Kitchen WorkerBroker polling model",
				},
			},
		},
	}
}
