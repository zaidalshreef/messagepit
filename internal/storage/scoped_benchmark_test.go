package storage

import (
	"context"
	"testing"
)

func BenchmarkScopedEmailCapture(b *testing.B) {
	setup("bench_scoped_capture")
	defer Close()
	ctx := context.Background()
	group, err := CreateGroup(ctx, "Benchmark")
	if err != nil {
		b.Fatal(err)
	}
	project, err := CreateProject(ctx, group.ID, "Benchmark")
	if err != nil {
		b.Fatal(err)
	}
	envs, err := ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(envs) != 1 {
		b.Fatalf("environments: %+v %v", envs, err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := StoreInEnvironment(ctx, envs[0].ID, &testTextEmail, nil); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
}
