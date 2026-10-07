package main

import (
	"github.com/hyscaler/qavia/api/internal/analyses"
	"github.com/hyscaler/qavia/api/internal/artifacts"
	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/auth"
	"github.com/hyscaler/qavia/api/internal/coverage"
	"github.com/hyscaler/qavia/api/internal/datagen"
	"github.com/hyscaler/qavia/api/internal/defects"
	"github.com/hyscaler/qavia/api/internal/generation"
	"github.com/hyscaler/qavia/api/internal/health"
	"github.com/hyscaler/qavia/api/internal/integrations"
	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/mcp"
	"github.com/hyscaler/qavia/api/internal/mocks"
	"github.com/hyscaler/qavia/api/internal/notifications"
	"github.com/hyscaler/qavia/api/internal/perf"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/projects"
	"github.com/hyscaler/qavia/api/internal/reports"
	"github.com/hyscaler/qavia/api/internal/repos"
	"github.com/hyscaler/qavia/api/internal/role"
	"github.com/hyscaler/qavia/api/internal/runs"
	"github.com/hyscaler/qavia/api/internal/security"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/setup"
	"github.com/hyscaler/qavia/api/internal/uitests"
	"github.com/hyscaler/qavia/api/internal/users"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// server satisfies the generated StrictServerInterface by embedding one handler
// per domain package.
//
// Each domain package implements its own slice of the interface
// (backend-standards.md 1), and this struct is the only place they are combined.
// The compiler enforces completeness: adding a path to qavia.yaml breaks this
// build until some handler implements it.
// Each package names its type Handler, per the layout convention, so embedding
// them directly would give three fields called Handler. Aliases give the embedded
// fields distinct names while keeping method promotion, which is what makes the
// compile-time completeness check above possible without writing eleven forwarding
// methods by hand.
type (
	healthAPI        = health.Handler
	authAPI          = auth.Handler
	usersAPI         = users.Handler
	settingsAPI      = settings.Handler
	setupAPI         = setup.Handler
	projectsAPI      = projects.Handler
	artifactsAPI     = artifacts.Handler
	jobsAPI          = jobs.Handler
	aiAPI            = llm.Handler
	generationAPI    = generation.Handler
	executionAPI     = runs.Handler
	analysisAPI      = analyses.Handler
	defectsAPI       = defects.Handler
	reportsAPI       = reports.Handler
	repositoryAPI    = repos.Handler
	coverageAPI      = coverage.Handler
	uiTestsAPI       = uitests.Handler
	testDataAPI      = datagen.Handler
	mocksAPI         = mocks.Handler
	mcpAPI           = mcp.Handler
	integrationsAPI  = integrations.Handler
	perfAPI          = perf.Handler
	securityAPI      = security.Handler
	webhooksAPI      = jobs.WebhookHandler
	notificationsAPI = notifications.Handler
	auditAPI         = audit.Handler
)

type server struct {
	*healthAPI
	*authAPI
	*usersAPI
	*settingsAPI
	*setupAPI
	*projectsAPI
	*artifactsAPI
	*jobsAPI
	*aiAPI
	*generationAPI
	*executionAPI
	*analysisAPI
	*defectsAPI
	*reportsAPI
	*repositoryAPI
	*coverageAPI
	*uiTestsAPI
	*testDataAPI
	*mocksAPI
	*mcpAPI
	*integrationsAPI
	*perfAPI
	*securityAPI
	*webhooksAPI
	*notificationsAPI
	*auditAPI
}

// Compile-time proof that every operation in the spec has an implementation. This
// line is the whole reason strict-server is worth its verbosity.
var _ api.StrictServerInterface = (*server)(nil)

// policies declares who may call each operation.
//
// Deny by default: an operation with no entry here is refused, and a test asserts
// every operation in the spec appears, so a new endpoint cannot ship without an
// explicit authorisation decision.
//
// Roles are listed as an exact set rather than a floor. Adding a role to the
// platform must not silently widen access to every route that allowed the role
// below it.
func policies() httpx.PolicySet {
	// Named for readability at the call sites below. admins is spelled out rather
	// than implied by a hierarchy check.
	var (
		admins = []role.Role{role.Admin}

		// leads administer a project: who is on it, and whether it is archived.
		leads = []role.Role{role.Admin, role.QALead}

		// contributors do the work: create projects, upload inputs, submit runs.
		// A Viewer is deliberately absent, which is what makes Viewer read-only
		// without a second check inside every service.
		contributors = []role.Role{role.Admin, role.QALead, role.QAEngineer}

		everyone []role.Role // any authenticated user
	)

	return httpx.PolicySet{
		// Probes must answer before there is a session, and an orchestrator has
		// no credentials to offer.
		"getLiveness":  {Public: true},
		"getReadiness": {Public: true},

		// Public because they are how a caller obtains a session in the first
		// place. Both are rate limited, and both refuse to say whether an account
		// or a token exists.
		"login":        {Public: true},
		"acceptInvite": {Public: true},

		// First-run setup runs before any account exists, so it cannot require one.
		// The admin endpoint stops existing the moment a user does, which is what
		// keeps it from being a permanent unauthenticated account-creation route.
		"getSetupStatus":   {Public: true},
		"createFirstAdmin": {Public: true},

		// The inbound webhook has no session: its credential is an HMAC over the
		// body, checked against a per-project secret.
		"triggerWebhook": {Public: true},

		// Any signed-in user, whatever their role.
		"logout":         {Roles: everyone},
		"getCurrentUser": {Roles: everyone},
		"changePassword": {Roles: everyone},

		// User administration. Admin only, including reading the list: who works
		// here and in what role is not something a Viewer needs.
		"listUsers":          {Roles: admins},
		"inviteUser":         {Roles: admins},
		"setUserRole":        {Roles: admins},
		"setUserStatus":      {Roles: admins},
		"revokeUserSessions": {Roles: admins},
		"unlockUser":         {Roles: admins},

		// Settings are readable by any signed-in user and writable by any signed-in
		// user, because everyone owns their own preferences. The per-setting minimum
		// role is enforced inside the service, per key, which is finer than a route
		// could be: one endpoint carries changes to both a user preference and a
		// global runner limit.
		"getSettingsRegistry": {Roles: everyone},
		"getSettings":         {Roles: everyone},
		"updateSettings":      {Roles: everyone},
		"clearSetting":        {Roles: everyone},

		// Projects. Reading is open to anybody signed in and then narrowed to
		// membership by the middleware; changing what a project is, is not.
		"listProjects":          {Roles: everyone},
		"getProject":            {Roles: everyone},
		"createProject":         {Roles: contributors},
		"updateProject":         {Roles: contributors},
		"archiveProject":        {Roles: leads},
		"unarchiveProject":      {Roles: leads},
		"listProjectMembers":    {Roles: everyone},
		"addProjectMember":      {Roles: leads},
		"removeProjectMember":   {Roles: leads},
		"setExternalAIApproval": {Roles: admins},

		// Inputs. Uploading changes what will be generated, so it is a
		// contributor's action; reading one is not.
		"listArtifacts":        {Roles: everyone},
		"getArtifact":          {Roles: everyone},
		"listArtifactVersions": {Roles: everyone},
		"downloadArtifact":     {Roles: everyone},
		"uploadArtifact":       {Roles: contributors},
		"deleteArtifact":       {Roles: contributors},

		// Jobs. Watching costs nothing; starting and stopping work does.
		"listJobs":        {Roles: everyone},
		"getJob":          {Roles: everyone},
		"streamJobEvents": {Roles: everyone},
		"submitJob":       {Roles: contributors},
		"cancelJob":       {Roles: contributors},

		// Everybody reads their own notifications, and only their own: the user ID
		// is part of every query rather than a filter the handler applies.
		"listNotifications":          {Roles: everyone},
		"getUnreadNotificationCount": {Roles: everyone},
		"markNotificationRead":       {Roles: everyone},
		"markAllNotificationsRead":   {Roles: everyone},

		// The AI layer is admin only, all of it. A provider row holds credentials,
		// a model row holds prices, and a tier assignment decides where client code
		// is sent: none of that is a QA Engineer's decision (BE-1.13).
		"listAIProviders":  {Roles: admins},
		"createAIProvider": {Roles: admins},
		"getAIProvider":    {Roles: admins},
		"updateAIProvider": {Roles: admins},
		"deleteAIProvider": {Roles: admins},
		"testAIProvider":   {Roles: admins},
		"listAIModels":     {Roles: admins},
		"createAIModel":    {Roles: admins},
		"updateAIModel":    {Roles: admins},
		"deleteAIModel":    {Roles: admins},
		"listAITiers":      {Roles: admins},
		"assignAITier":     {Roles: admins},
		"unassignAITier":   {Roles: admins},
		"getAIBudget":      {Roles: admins},
		"updateAIBudget":   {Roles: admins},

		// Spend is admin only too. It is a cost report across every project, and a
		// QA Lead sees their own project's cost estimate on the generation screen.
		"getAISpend": {Roles: admins},

		// Generation. Reading what was understood and what was designed is open to
		// anybody on the project; changing the suite is a contributor's action, and
		// the estimate is grouped with the writes because it names what the run will
		// cost somebody money.
		"listRequirements":       {Roles: everyone},
		"listEndpoints":          {Roles: everyone},
		"getRequirementCoverage": {Roles: everyone},
		"listTestCases":          {Roles: everyone},
		"getTestCase":            {Roles: everyone},
		"estimateGeneration":     {Roles: contributors},
		"createTestCase":         {Roles: contributors},
		"updateTestCase":         {Roles: contributors},
		"deleteTestCase":         {Roles: contributors},
		"setTestCaseStatuses":    {Roles: contributors},

		// Generated files. Reading and exporting are open to the project; writing
		// the suite is a contributor's action, and the Postman export writes a file.
		"listTestFiles":           {Roles: everyone},
		"getTestFile":             {Roles: everyone},
		"downloadTestFile":        {Roles: everyone},
		"listFilesForTestCase":    {Roles: everyone},
		"exportTestFiles":         {Roles: everyone},
		"exportPostmanCollection": {Roles: contributors},

		// Execution. Reading a run is open to the project, because a result is what
		// the project exists to produce; starting one is a contributor's action, and
		// cancelling one is too. There is no admin-only run endpoint: an operator who
		// needs to stop everything changes the concurrency limit or the driver
		// setting, which is a settings change and is audited as one.
		"triggerRun":          {Roles: contributors},
		"cancelRun":           {Roles: contributors},
		"listRuns":            {Roles: everyone},
		"getRun":              {Roles: everyone},
		"listRunResults":      {Roles: everyone},
		"listRunCommands":     {Roles: everyone},
		"streamRunLogs":       {Roles: everyone},
		"getRunTrend":         {Roles: everyone},
		"getProjectDashboard": {Roles: everyone},
		"getTestCaseHistory":  {Roles: everyone},

		// Analysis and defects. Reading an explanation is open to the project; asking
		// for one costs provider calls, and filing or moving a defect is doing work.
		// The feedback export is admin only: it is a platform-wide view across every
		// project's analyses.
		"listRunAnalyses":            {Roles: everyone},
		"getResultAnalysis":          {Roles: everyone},
		"analyseRun":                 {Roles: contributors},
		"setAnalysisFeedback":        {Roles: everyone},
		"clearAnalysisFeedback":      {Roles: everyone},
		"getFeedbackByPromptVersion": {Roles: admins},

		"listDefects":         {Roles: everyone},
		"getDefect":           {Roles: everyone},
		"listDefectComments":  {Roles: everyone},
		"createDefect":        {Roles: contributors},
		"updateDefect":        {Roles: contributors},
		"linkDefectDuplicate": {Roles: contributors},
		"commentOnDefect":     {Roles: contributors},
		"promoteResult":       {Roles: contributors},

		// Reports. Generating one costs a job and, for a PDF, a container; reading and
		// downloading is open to the project.
		"requestReport":  {Roles: contributors},
		"listReports":    {Roles: everyone},
		"getReport":      {Roles: everyone},
		"downloadReport": {Roles: everyone},

		// Repositories. Reading the connection is open to the project; connecting one
		// names an external system and stores a credential, so it is a lead's call.
		"getRepository":        {Roles: everyone},
		"connectRepository":    {Roles: leads},
		"disconnectRepository": {Roles: leads},
		"syncRepository":       {Roles: contributors},
		"mapRepository":        {Roles: contributors},
		"getRepositoryMap":     {Roles: everyone},
		"generateUnitTests":    {Roles: contributors},

		// UI discovery. Reading a graph is open to the project; running a discovery
		// drives a browser against a live environment and costs a container, so it is a
		// contributor's call. Marking one reviewed is a statement somebody made, so it
		// needs a person who could act on it.
		"discoverUIFlows":      {Roles: contributors},
		"listUIFlows":          {Roles: everyone},
		"getLatestUIFlowGraph": {Roles: everyone},
		"getUIFlowGraph":       {Roles: everyone},
		"reviewUIFlowGraph":    {Roles: contributors},
		"generateUITests":      {Roles: contributors},

		// Test data. Generating it reads the specification and writes nothing, so it is
		// open to anyone on the project: a viewer debugging a failure needs the same
		// fixture the engineer used.
		"listTestDataShapes":      {Roles: everyone},
		"generateTestData":        {Roles: everyone},
		"generateInvalidTestData": {Roles: everyone},
		"listDumpTables":          {Roles: everyone},

		// The mock server. Reading its status is open to the project; starting one runs a
		// container and publishes a port, and stopping one takes a URL out from under
		// whoever is pointed at it.
		"getMockServer":   {Roles: everyone},
		"startMockServer": {Roles: contributors},
		"stopMockServer":  {Roles: contributors},

		// A run's recordings. Reading them is reading the run, which is open to the
		// project: a video of a failure is evidence, and evidence a viewer cannot see is
		// evidence nobody acts on.
		"startPerformanceTest": {Roles: contributors},
		"getRunMetrics":        {Roles: everyone},
		"startSecurityScan":    {Roles: contributors},
		"listRunFindings":      {Roles: everyone},

		// MCP servers. Admin only: an MCP server is a credentialed channel to an external
		// system that can change state, and configuring one is exactly the decision that
		// belongs to an admin (ai-architecture.md 5.5).
		"listMCPServers":  {Roles: admins},
		"createMCPServer": {Roles: admins},
		"getMCPServer":    {Roles: admins},
		"updateMCPServer": {Roles: admins},
		"deleteMCPServer": {Roles: admins},
		"testMCPServer":   {Roles: admins},
		"listMCPCalls":    {Roles: admins},

		// The integration health report. Admin only: it names external systems and their
		// configured state.
		"listIntegrations": {Roles: admins},

		"listRunArtifacts": {Roles: everyone},

		// Quarantine. Reading the list is open to the project; excusing a test, claiming
		// one, and ending one are decisions about whether the suite fails, so they need
		// somebody who could act on the test itself.
		"listQuarantines":       {Roles: everyone},
		"quarantineTest":        {Roles: contributors},
		"assignQuarantineOwner": {Roles: contributors},
		"releaseQuarantine":     {Roles: contributors},
		"downloadRunArtifact":   {Roles: everyone},

		// Measured coverage. Reading is open to the project; measuring runs a client's
		// whole test suite in a container and costs a runner slot.
		"getCodeCoverage": {Roles: everyone},
		"measureCoverage": {Roles: contributors},

		// The audit log is admin only. It records who did what, including to whom.
		"listAuditEntries": {Roles: admins},
	}
}
