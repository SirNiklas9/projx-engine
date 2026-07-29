package confineext_test

import (
	"testing"

	_ "github.com/BananaLabs-OSS/Pulp-ext-confine"
	"github.com/BananaLabs-OSS/Pulp/ext"
)

// TestCapabilityRegistered verifies that importing the package registers the
// spawn.confine capability with the Pulp extension registry.
func TestCapabilityRegistered(t *testing.T) {
	caps := ext.All()
	for _, c := range caps {
		if c.Name == "spawn.confine" {
			t.Logf("spawn.confine registered: Setup=%v Teardown=%v Register=%v Stub=%v",
				c.Setup != nil, c.Teardown != nil, c.Register != nil, c.Stub != nil)
			return
		}
	}
	t.Fatal("spawn.confine not found in ext.All()")
}
