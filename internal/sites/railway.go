package sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The client's service in the Clients project, through Railway's public
// GraphQL API (https://docs.railway.com/integrations/api). Every step is
// idempotent: the provisioner (internal/provision) records each result in
// account_sites and calls again until the site is live.

// gql runs one GraphQL operation; out receives `data`.
func (c Config) gql(ctx context.Context, query string, vars map[string]any, out any) error {
	var res struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	err := c.post(ctx, c.RailwayURL, map[string]string{"Authorization": "Bearer " + c.RailwayToken},
		map[string]any{"query": query, "variables": vars}, &res)
	if err != nil {
		return fmt.Errorf("sites: railway: %w", err)
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("sites: railway: %s", res.Errors[0].Message)
	}
	if out != nil && len(res.Data) > 0 {
		return json.Unmarshal(res.Data, out)
	}
	return nil
}

const projectServicesQuery = `query($id: String!) { project(id: $id) { services { edges { node { id name } } } } }`

// findService returns the id of the Clients service called name ("" if none).
func (c Config) findService(ctx context.Context, name string) (string, error) {
	var out struct {
		Project struct {
			Services struct {
				Edges []struct {
					Node struct{ ID, Name string } `json:"node"`
				} `json:"edges"`
			} `json:"services"`
		} `json:"project"`
	}
	if err := c.gql(ctx, projectServicesQuery, map[string]any{"id": c.ClientsProject}, &out); err != nil {
		return "", err
	}
	for _, e := range out.Project.Services.Edges {
		if e.Node.Name == name {
			return e.Node.ID, nil
		}
	}
	return "", nil
}

const serviceCreateMutation = `mutation($input: ServiceCreateInput!) { serviceCreate(input: $input) { id name } }`

// EnsureService returns the id of the account's service (site-<account_id>)
// in the Clients project, creating it empty when it doesn't exist. Looking
// it up by name first makes a retry after a crash reuse the service.
func (c Config) EnsureService(ctx context.Context, accountID string) (id, name string, err error) {
	if !c.Railway() {
		return "", "", fmt.Errorf("sites: faltan %s", c.Missing())
	}
	name = ServiceName(accountID)
	if id, err = c.findService(ctx, name); err != nil || id != "" {
		return id, name, err
	}
	var out struct {
		ServiceCreate struct{ ID string } `json:"serviceCreate"`
	}
	// No source yet: variables and root directory go first, so the first
	// deployment is already the right one.
	if err := c.gql(ctx, serviceCreateMutation, map[string]any{"input": map[string]any{
		"projectId": c.ClientsProject, "name": name}}, &out); err != nil {
		return "", name, err
	}
	if out.ServiceCreate.ID == "" {
		return "", name, errors.New("sites: railway: serviceCreate returned no id")
	}
	return out.ServiceCreate.ID, name, nil
}

const variablesMutation = `mutation($input: VariableCollectionUpsertInput!) { variableCollectionUpsert(input: $input) }`

const instanceUpdateMutation = `mutation($serviceId: String!, $environmentId: String!, $input: ServiceInstanceUpdateInput!) {
  serviceInstanceUpdate(serviceId: $serviceId, environmentId: $environmentId, input: $input)
}`

// ServiceVariables is what the client's service runs with: the template in
// single-site mode for its site_id.
func (c Config) ServiceVariables(siteID string) map[string]string {
	return map[string]string{
		"SITE_MODE": "site", "SITE_ID": siteID, "ANDRHO_API_URL": c.APIURL, "PORT": strconv.Itoa(c.Port),
	}
}

// ConfigureService sets the service's variables and root directory.
func (c Config) ConfigureService(ctx context.Context, serviceID, siteID string) error {
	if c.APIURL == "" {
		return errors.New("sites: falta SITES_API_URL (la URL pública de andrho-api para el sitio)")
	}
	if err := c.gql(ctx, variablesMutation, map[string]any{"input": map[string]any{
		"projectId": c.ClientsProject, "environmentId": c.ClientsEnv, "serviceId": serviceID,
		"variables": c.ServiceVariables(siteID), "skipDeploys": true}}, nil); err != nil {
		return err
	}
	return c.gql(ctx, instanceUpdateMutation, map[string]any{"serviceId": serviceID, "environmentId": c.ClientsEnv,
		"input": map[string]any{"rootDirectory": c.TemplateRoot}}, nil)
}

const serviceConnectMutation = `mutation($id: String!, $input: ServiceConnectInput!) { serviceConnect(id: $id, input: $input) { id } }`

const deployMutation = `mutation($serviceId: String!, $environmentId: String!) {
  serviceInstanceDeployV2(serviceId: $serviceId, environmentId: $environmentId)
}`

// Deploy connects the service to the template's repo (which deploys it), or
// deploys it again when it's already connected. Only called while the
// service has no deployment.
func (c Config) Deploy(ctx context.Context, serviceID string, connected bool) error {
	if connected {
		return c.gql(ctx, deployMutation, map[string]any{"serviceId": serviceID, "environmentId": c.ClientsEnv}, nil)
	}
	return c.gql(ctx, serviceConnectMutation, map[string]any{"id": serviceID, "input": map[string]any{
		"repo": c.TemplateRepo, "branch": c.TemplateBranch}}, nil)
}

const domainsQuery = `query($projectId: String!, $environmentId: String!, $serviceId: String!) {
  domains(projectId: $projectId, environmentId: $environmentId, serviceId: $serviceId) { serviceDomains { id domain } }
}`

const serviceDomainDeleteMutation = `mutation($id: String!) { serviceDomainDelete(id: $id) }`

// RemovePublicDomains deletes every *.up.railway.app domain of the service:
// the site must only answer through sites-router, or Google would see the
// same content twice. Returns how many it removed.
func (c Config) RemovePublicDomains(ctx context.Context, serviceID string) (int, error) {
	var out struct {
		Domains struct {
			ServiceDomains []struct{ ID, Domain string } `json:"serviceDomains"`
		} `json:"domains"`
	}
	if err := c.gql(ctx, domainsQuery, map[string]any{"projectId": c.ClientsProject, "environmentId": c.ClientsEnv,
		"serviceId": serviceID}, &out); err != nil {
		return 0, err
	}
	n := 0
	for _, d := range out.Domains.ServiceDomains {
		if err := c.gql(ctx, serviceDomainDeleteMutation, map[string]any{"id": d.ID}, nil); err != nil {
			return n, fmt.Errorf("sites: remove %s: %w", d.Domain, err)
		}
		n++
	}
	return n, nil
}

const latestDeploymentQuery = `query($serviceId: String!, $environmentId: String!) {
  serviceInstance(serviceId: $serviceId, environmentId: $environmentId) { latestDeployment { id status } source { repo } }
}`

// Deployment is the state of the service's latest deployment.
type Deployment struct {
	// Status is Railway's (SUCCESS, BUILDING, DEPLOYING, FAILED, CRASHED…),
	// "" when the service has never been deployed.
	Status string
	// Connected: the service has the template's repo as its source.
	Connected bool
}

// Done reports a deployment that finished well.
func (d Deployment) Done() bool { return d.Status == "SUCCESS" }

// Failed reports a deployment that won't finish by itself.
func (d Deployment) Failed() bool {
	switch d.Status {
	case "FAILED", "CRASHED", "REMOVED":
		return true
	}
	return false
}

// LatestDeployment reads the service's latest deployment.
func (c Config) LatestDeployment(ctx context.Context, serviceID string) (Deployment, error) {
	var out struct {
		ServiceInstance struct {
			LatestDeployment *struct{ ID, Status string } `json:"latestDeployment"`
			Source           *struct{ Repo string }       `json:"source"`
		} `json:"serviceInstance"`
	}
	if err := c.gql(ctx, latestDeploymentQuery, map[string]any{"serviceId": serviceID, "environmentId": c.ClientsEnv}, &out); err != nil {
		return Deployment{}, err
	}
	d := Deployment{}
	if ld := out.ServiceInstance.LatestDeployment; ld != nil {
		d.Status = strings.ToUpper(ld.Status)
	}
	if src := out.ServiceInstance.Source; src != nil && src.Repo != "" {
		d.Connected = true
	}
	return d, nil
}
