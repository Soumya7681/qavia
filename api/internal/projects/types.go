package projects

import (
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/hyscaler/qavia/api/internal/role"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// TestType is a kind of test a project generates (FR-1.2).
type TestType string

const (
	TestTypeTestCases   TestType = "test_cases"
	TestTypeAPITests    TestType = "api_tests"
	TestTypeUnitTests   TestType = "unit_tests"
	TestTypeUITests     TestType = "ui_tests"
	TestTypePerformance TestType = "performance_tests"
	TestTypeSecurity    TestType = "security_tests"
	TestTypeTestData    TestType = "test_data"
	TestTypeMockServer  TestType = "mock_server"
)

// unavailableReason says why a test type cannot be acted on yet, phrased for the
// person reading it.
//
// A type absent from this map is implemented. Types are returned to the client
// with the reason attached rather than hidden, so the UI disables the option and
// says when it arrives instead of pretending it does not exist (F-3.12). Landing a
// phase means deleting one line here.
var unavailableReason = map[TestType]string{
	TestTypeTestCases:   "Test case generation arrives in phase 2.",
	TestTypeAPITests:    "API test generation arrives in phase 3.",
	TestTypeUnitTests:   "Unit test generation arrives in phase 6.",
	TestTypeUITests:     "UI test generation arrives in phase 7.",
	TestTypePerformance: "Performance testing arrives in phase 9.",
	TestTypeSecurity:    "Security testing arrives in phase 9.",
	TestTypeTestData:    "Test data generation arrives in phase 8.",
	TestTypeMockServer:  "The mock server arrives in phase 8.",
}

// AllTestTypes is every selectable type, in the order a form should show them.
var AllTestTypes = []TestType{
	TestTypeTestCases, TestTypeAPITests, TestTypeUnitTests, TestTypeUITests,
	TestTypePerformance, TestTypeSecurity, TestTypeTestData, TestTypeMockServer,
}

// Valid reports whether the value is a known test type.
func (t TestType) Valid() bool {
	_, known := unavailableReason[t]
	if known {
		return true
	}
	for _, candidate := range AllTestTypes {
		if candidate == t {
			return true
		}
	}
	return false
}

// Available reports whether the platform can act on the type yet, and why not.
func (t TestType) Available() (bool, string) {
	reason, unavailable := unavailableReason[t]
	return !unavailable, reason
}

// Project is a project as the rest of the platform sees it.
type Project struct {
	ID          uuid.UUID
	Name        string
	Description string
	OwnerID     uuid.UUID

	TestTypes []TestType

	// ExternalAIApproved is the server-side data-residency gate. Without it the
	// project may only be assigned providers marked local (F-16.13).
	ExternalAIApproved bool

	ArchivedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Archived reports whether the project is read-only.
func (p Project) Archived() bool { return p.ArchivedAt != nil }

// Member is somebody with access to a project.
type Member struct {
	UserID uuid.UUID
	Email  string
	Name   string

	// Role is the project role, which is separate from the platform role. The
	// narrower of the two applies.
	Role role.Role

	// IsOwner is true for the creator, whose access does not depend on a
	// membership row existing.
	IsOwner   bool
	CreatedAt time.Time
}

// Page is one page of projects plus the cursor for the next.
type Page struct {
	Items      []Project
	NextCursor string
}

func toProject(row dbgen.Project) Project {
	types := make([]TestType, 0, len(row.TestTypes))
	for _, value := range row.TestTypes {
		types = append(types, TestType(value))
	}

	return Project{
		ID:                 row.ID,
		Name:               row.Name,
		Description:        row.Description,
		OwnerID:            row.OwnerID,
		TestTypes:          types,
		ExternalAIApproved: row.ExternalAiApproved,
		ArchivedAt:         row.ArchivedAt,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}

// ToAPI maps a project to its response shape. Every response goes through a
// mapper, so adding a column cannot silently change the API (backend-standards.md 4).
func ToAPI(p Project) api.Project {
	out := api.Project{
		Id:                 p.ID,
		Name:               p.Name,
		Description:        p.Description,
		OwnerId:            p.OwnerID,
		ExternalAiApproved: p.ExternalAIApproved,
		Archived:           p.Archived(),
		TestTypes:          make([]api.TestTypeSelection, 0, len(p.TestTypes)),
		CreatedAt:          p.CreatedAt,
		UpdatedAt:          p.UpdatedAt,
	}

	for _, testType := range p.TestTypes {
		available, reason := testType.Available()
		selection := api.TestTypeSelection{
			Type:      api.TestType(testType),
			Available: available,
		}
		if !available {
			selection.Reason = &reason
		}
		out.TestTypes = append(out.TestTypes, selection)
	}

	if p.ArchivedAt != nil {
		out.ArchivedAt.Set(*p.ArchivedAt)
	}
	return out
}

// ToAPIMember maps a member to its response shape.
func ToAPIMember(m Member) api.ProjectMember {
	return api.ProjectMember{
		UserId:    m.UserID,
		Email:     openapi_types.Email(m.Email),
		Name:      m.Name,
		Role:      api.Role(m.Role),
		IsOwner:   m.IsOwner,
		CreatedAt: m.CreatedAt,
	}
}
