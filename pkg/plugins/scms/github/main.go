package github

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"golang.org/x/oauth2"

	"github.com/shurcooL/githubv4"

	"github.com/updatecli/updatecli/pkg/core/tmp"
	"github.com/updatecli/updatecli/pkg/plugins/scms/git/commit"
	"github.com/updatecli/updatecli/pkg/plugins/scms/git/sign"

	"github.com/updatecli/updatecli/pkg/plugins/utils/gitgeneric"
)

// Spec represents the configuration input
type Spec struct {
	/*
		"branch" defines the git branch to work on.

		compatible:
			* scm

		default:
			main

		remark:
			depending on which resource references the GitHub scm, the behavior will be different.

			If the scm is linked to a source or a condition (using scmid), the branch will be used to retrieve
			file(s) from that branch.

			If the scm is linked to target then Updatecli creates a new "working branch" based on the branch value.
			The working branch created by Updatecli looks like "updatecli_<pipelineID>".
			It is worth mentioning that it is not possible to by pass the working branch in the current situation.
			For more information, please refer to the following issue:
			https://github.com/updatecli/updatecli/issues/1139

			If you need to push changes to a specific branch, you must use the plugin "git" instead of this

	*/
	Branch string `yaml:",omitempty"`
	/*
		"directory" defines the local path where the git repository is cloned.

		compatible:
			* scm

		remark:
			Unless you know what you are doing, it is recommended to use the default value.
			The reason is that Updatecli may automatically clean up the directory after a pipeline execution.

		default:
			/tmp/updatecli/github/<owner>/<repository>
	*/
	Directory string `yaml:",omitempty"`
	/*
		"email" defines the email used to commit changes.

		compatible:
			* scm

		default:
			default set to your global git configuration
	*/
	Email string `yaml:",omitempty"`
	/*
		"owner" defines the owner of a repository.

		compatible:
			* scm
	*/
	Owner string `yaml:",omitempty" jsonschema:"required"`
	/*
		repository specifies the name of a repository for a specific owner.

		compatible:
			* scm
	*/
	Repository string `yaml:",omitempty" jsonschema:"required"`
	/*
		"token" specifies the credential used to authenticate with GitHub API.

		compatible:
			* scm
	*/
	Token string `yaml:",omitempty" jsonschema:"required"`
	/*
		url specifies the default github url in case of GitHub enterprise

		compatible:
			* scm

		default:
			github.com
	*/
	URL string `yaml:",omitempty"`
	/*
		"username" specifies the username used to authenticate with GitHub API.

		compatible:
			* scm

		remark:
			the token is usually enough to authenticate with GitHub API.
	*/
	Username string `yaml:",omitempty"`
	/*
		"user" specifies the user associated with new git commit messages created by Updatecli

		compatible:
			* scm
	*/
	User string `yaml:",omitempty"`
	/*
		"gpg" specifies the GPG key and passphrased used for commit signing

		compatible:
			* scm
	*/
	GPG sign.GPGSpec `yaml:",omitempty"`
	/*
		"force" is used during the git push phase to run `git push --force`.

		compatible:
			* scm

		default:
			false
	*/
	Force bool `yaml:",omitempty"`
	/*
		"commitMessage" is used to generate the final commit message.

		compatible:
			* scm

		remark:
			it's worth mentioning that the commit message settings is applied to all targets linked to the same scm.
	*/
	CommitMessage commit.Commit `yaml:",omitempty"`
}

// GitHub contains settings to interact with GitHub
type Github struct {
	// Spec contains inputs coming from updatecli configuration
	Spec             Spec
	pipelineID       string
	client           GitHubClient
	nativeGitHandler gitgeneric.GitHandler
	mu               sync.RWMutex
}

// Repository contains GitHub repository data
type Repository struct {
	ID          string
	Name        string
	Owner       string
	ParentID    string
	ParentName  string
	ParentOwner string
}

// New returns a new valid GitHub object.
func New(s Spec, pipelineID string) (*Github, error) {
	errs := s.Validate()

	if len(errs) > 0 {
		strErrs := []string{}
		for _, err := range errs {
			strErrs = append(strErrs, err.Error())
		}
		return &Github{}, fmt.Errorf(strings.Join(strErrs, "\n"))
	}

	if s.Directory == "" {
		s.Directory = path.Join(tmp.Directory, "github", s.Owner, s.Repository)
	}

	if s.URL == "" {
		s.URL = "github.com"
	}

	if !strings.HasPrefix(s.URL, "https://") && !strings.HasPrefix(s.URL, "http://") {
		s.URL = "https://" + s.URL
	}

	// Initialize github client
	src := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: s.Token},
	)
	httpClient := oauth2.NewClient(context.Background(), src)
	nativeGitHandler := gitgeneric.GoGit{}

	g := Github{
		Spec:             s,
		pipelineID:       pipelineID,
		nativeGitHandler: nativeGitHandler,
	}

	if strings.HasSuffix(s.URL, "github.com") {
		g.client = githubv4.NewClient(httpClient)
	} else {
		// For GH enterprise the GraphQL API path is /api/graphql
		// Cf https://docs.github.com/en/enterprise-cloud@latest/graphql/guides/managing-enterprise-accounts#3-setting-up-insomnia-to-use-the-github-graphql-api-with-enterprise-accounts
		graphqlURL, err := url.JoinPath(s.URL, "/api/graphql")
		if err != nil {
			return nil, err
		}
		g.client = githubv4.NewEnterpriseClient(graphqlURL, httpClient)
	}

	g.setDirectory()

	return &g, nil
}

// Validate verifies if mandatory GitHub parameters are provided and return false if not.
func (s *Spec) Validate() (errs []error) {
	required := []string{}

	if len(s.Token) == 0 {
		required = append(required, "token")
	}

	if len(s.Owner) == 0 {
		required = append(required, "owner")
	}

	if len(s.Repository) == 0 {
		required = append(required, "repository")
	}

	if len(required) > 0 {
		errs = append(errs, fmt.Errorf("github parameter(s) required: [%v]", strings.Join(required, ",")))
	}

	return errs
}

// Merge returns nil if it successfully merges the child Spec into target receiver.
// Please note that child attributes always overrides receiver's
func (gs *Spec) Merge(child interface{}) error {
	childGHSpec, ok := child.(Spec)
	if !ok {
		return fmt.Errorf("unable to merge GitHub spec with unknown object type")
	}

	if childGHSpec.Branch != "" {
		gs.Branch = childGHSpec.Branch
	}
	if childGHSpec.CommitMessage != (commit.Commit{}) {
		gs.CommitMessage = childGHSpec.CommitMessage
	}
	if childGHSpec.Directory != "" {
		gs.Directory = childGHSpec.Directory
	}
	if childGHSpec.Email != "" {
		gs.Email = childGHSpec.Email
	}
	if childGHSpec.Force {
		gs.Force = childGHSpec.Force
	}
	if childGHSpec.GPG != (sign.GPGSpec{}) {
		gs.GPG = childGHSpec.GPG
	}
	if childGHSpec.Owner != "" {
		gs.Owner = childGHSpec.Owner
	}
	// PullRequest is deprecated so not merging it
	if childGHSpec.Repository != "" {
		gs.Repository = childGHSpec.Repository
	}
	if childGHSpec.Token != "" {
		gs.Token = childGHSpec.Token
	}
	if childGHSpec.URL != "" {
		gs.URL = childGHSpec.URL
	}
	if childGHSpec.User != "" {
		gs.User = childGHSpec.User
	}
	if childGHSpec.Username != "" {
		gs.Username = childGHSpec.Username
	}

	return nil
}

// MergeFromEnv updates the target receiver with the "non zero-ed" environment variables
func (gs *Spec) MergeFromEnv(envPrefix string) {
	prefix := fmt.Sprintf("%s_", envPrefix)
	if os.Getenv(fmt.Sprintf("%s%s", prefix, "BRANCH")) != "" {
		gs.Branch = os.Getenv(fmt.Sprintf("%s%s", prefix, "BRANCH"))
	}
	if os.Getenv(fmt.Sprintf("%s%s", prefix, "DIRECTORY")) != "" {
		gs.Directory = os.Getenv(fmt.Sprintf("%s%s", prefix, "DIRECTORY"))
	}
	if os.Getenv(fmt.Sprintf("%s%s", prefix, "EMAIL")) != "" {
		gs.Email = os.Getenv(fmt.Sprintf("%s%s", prefix, "EMAIL"))
	}
	if os.Getenv(fmt.Sprintf("%s%s", prefix, "OWNER")) != "" {
		gs.Owner = os.Getenv(fmt.Sprintf("%s%s", prefix, "OWNER"))
	}
	if os.Getenv(fmt.Sprintf("%s%s", prefix, "REPOSITORY")) != "" {
		gs.Repository = os.Getenv(fmt.Sprintf("%s%s", prefix, "REPOSITORY"))
	}
	if os.Getenv(fmt.Sprintf("%s%s", prefix, "TOKEN")) != "" {
		gs.Token = os.Getenv(fmt.Sprintf("%s%s", prefix, "TOKEN"))
	}
	if os.Getenv(fmt.Sprintf("%s%s", prefix, "URL")) != "" {
		gs.URL = os.Getenv(fmt.Sprintf("%s%s", prefix, "URL"))
	}
	if os.Getenv(fmt.Sprintf("%s%s", prefix, "USERNAME")) != "" {
		gs.Username = os.Getenv(fmt.Sprintf("%s%s", prefix, "USERNAME"))
	}
	if os.Getenv(fmt.Sprintf("%s%s", prefix, "USER")) != "" {
		gs.User = os.Getenv(fmt.Sprintf("%s%s", prefix, "USER"))
	}
}

func (g *Github) setDirectory() {

	if _, err := os.Stat(g.Spec.Directory); os.IsNotExist(err) {

		err := os.MkdirAll(g.Spec.Directory, 0755)
		if err != nil {
			logrus.Errorf("err - %s", err)
		}
	}
}

func (g *Github) queryRepository() (*Repository, error) {
	/*
			   query($owner: String!, $name: String!) {
			       repository(owner: $owner, name: $name){
			           id
			           name
		               owner {
		                   login
		               }
			           parent {
		                   id
		                   name
		                   owner {
		                       login
		                   }
			           }
			       }
			   }
	*/

	var query struct {
		Repository struct {
			ID    string
			Name  string
			Owner struct {
				Login string
			}

			Parent *struct {
				ID    string
				Name  string
				Owner struct {
					Login string
				}
			}
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]interface{}{
		"owner": githubv4.String(g.Spec.Owner),
		"name":  githubv4.String(g.Spec.Repository),
	}

	err := g.client.Query(context.Background(), &query, variables)

	if err != nil {
		logrus.Errorf("err - %s", err)
		return nil, err
	}

	parentID := ""
	parentName := ""
	parentOwner := ""
	if query.Repository.Parent != nil {
		parentID = query.Repository.Parent.ID
		parentName = query.Repository.Parent.Name
		parentOwner = query.Repository.Parent.Owner.Login
	}

	result := &Repository{
		ID:          query.Repository.ID,
		Name:        query.Repository.Name,
		Owner:       query.Repository.Owner.Login,
		ParentID:    parentID,
		ParentName:  parentName,
		ParentOwner: parentOwner,
	}

	return result, nil
}
