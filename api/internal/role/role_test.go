package role

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAtLeast(t *testing.T) {
	require.True(t, Admin.AtLeast(Viewer))
	require.True(t, Admin.AtLeast(Admin))
	require.True(t, QALead.AtLeast(QAEngineer))
	require.False(t, QAEngineer.AtLeast(QALead))
	require.False(t, Viewer.AtLeast(QAEngineer))
}

// An unknown role must never be treated as a weak one. It is a bug, and every
// comparison against it fails closed.
func TestUnknownRoleFailsClosed(t *testing.T) {
	unknown := Role("superuser")

	require.False(t, unknown.Valid())
	require.False(t, unknown.AtLeast(Viewer))
	require.False(t, Admin.AtLeast(unknown))
}

func TestParse(t *testing.T) {
	for _, r := range All {
		parsed, err := Parse(string(r))
		require.NoError(t, err)
		require.Equal(t, r, parsed)
	}

	_, err := Parse("root")
	require.Error(t, err)
}

func TestLabels(t *testing.T) {
	require.Equal(t, "QA Lead", QALead.Label())
	require.Equal(t, []string{"Viewer", "Admin"}, Labels([]Role{Viewer, Admin}))
}

// All is ordered weakest first, and AtLeast depends on that.
func TestAllIsOrderedWeakestFirst(t *testing.T) {
	for i := 1; i < len(All); i++ {
		require.True(t, All[i].AtLeast(All[i-1]), "%s should outrank %s", All[i], All[i-1])
		require.False(t, All[i-1].AtLeast(All[i]))
	}
}
