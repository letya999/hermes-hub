package devcheck

import "testing"

func TestM32LifecycleRejectsIncompleteSelection(t *testing.T) {
	for _, item := range [][2]string{{"", "prod"}, {"image", ""}, {"image", "staging"}} {
		if err := M32Lifecycle(t.Context(), item[0], item[1]); err == nil {
			t.Fatal("incomplete M32 image/environment selection accepted")
		}
	}
}
