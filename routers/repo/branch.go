// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2018 The Gitea Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package repo

import (
	"strings"

	"code.gitea.io/gitea/models"
	"code.gitea.io/gitea/modules/auth"
	"code.gitea.io/gitea/modules/base"
	"code.gitea.io/gitea/modules/context"
	"code.gitea.io/gitea/modules/git"
	"code.gitea.io/gitea/modules/log"
	"code.gitea.io/gitea/modules/repofiles"
	repo_module "code.gitea.io/gitea/modules/repository"
	"code.gitea.io/gitea/modules/util"
	"code.gitea.io/gitea/routers/utils"
)

const (
	tplBranch base.TplName = "repo/branch/list"
)

// Branch contains the branch information
type Branch struct {
	Name              string
	Commit            *git.Commit
	IsProtected       bool
	IsDeleted         bool
	IsIncluded        bool
	DeletedBranch     *models.DeletedBranch
	CommitsAhead      int
	CommitsBehind     int
	LatestPullRequest *models.PullRequest
	MergeMovedOn      bool
}

// Branches render repository branch page
func Branches(ctx *context.Context) {
	page := ctx.QueryInt("page")
	if page <= 1 {
		page = 1
	}

	pageSize := ctx.QueryInt("limit")
	if pageSize <= 0 {
		// TODO Create constant for this (or is this fine?)
		pageSize = git.CommitsRangeSize
	}

	ctx.Data["Title"] = "Branches"
	ctx.Data["IsRepoToolbarBranches"] = true
	ctx.Data["DefaultBranch"] = repo_module.GetBranch(ctx.Repo.Repository, ctx.Repo.Repository.DefaultBranch)
	ctx.Data["AllowsPulls"] = ctx.Repo.Repository.AllowsPulls()
	ctx.Data["IsWriter"] = ctx.Repo.CanWrite(models.UnitTypeCode)
	ctx.Data["IsMirror"] = ctx.Repo.Repository.IsMirror
	ctx.Data["CanPull"] = ctx.Repo.CanWrite(models.UnitTypeCode) || (ctx.IsSigned && ctx.User.HasForkedRepo(ctx.Repo.Repository.ID))
	ctx.Data["PageIsViewCode"] = true
	ctx.Data["PageIsBranches"] = true

	branches, count := loadBranches(ctx, page, pageSize)
	ctx.Data["Branches"] = branches
	
	pager := context.NewPagination(count, git.CommitsRangeSize, page, 5)
	pager.SetDefaultParams(ctx)
	ctx.Data["Page"] = pager

	ctx.HTML(200, tplBranch)
}

// DeleteBranchPost responses for delete merged branch
func DeleteBranchPost(ctx *context.Context) {
	defer redirect(ctx)
	branchName := ctx.Query("name")
	if branchName == ctx.Repo.Repository.DefaultBranch {
		ctx.Flash.Error(ctx.Tr("repo.branch.default_deletion_failed", branchName))
		return
	}

	isProtected, err := ctx.Repo.Repository.IsProtectedBranch(branchName, ctx.User)
	if err != nil {
		log.Error("DeleteBranch: %v", err)
		ctx.Flash.Error(ctx.Tr("repo.branch.deletion_failed", branchName))
		return
	}

	if isProtected {
		ctx.Flash.Error(ctx.Tr("repo.branch.protected_deletion_failed", branchName))
		return
	}

	if !ctx.Repo.GitRepo.IsBranchExist(branchName) || branchName == ctx.Repo.Repository.DefaultBranch {
		ctx.Flash.Error(ctx.Tr("repo.branch.deletion_failed", branchName))
		return
	}

	if err := deleteBranch(ctx, branchName); err != nil {
		ctx.Flash.Error(ctx.Tr("repo.branch.deletion_failed", branchName))
		return
	}

	ctx.Flash.Success(ctx.Tr("repo.branch.deletion_success", branchName))
}

// RestoreBranchPost responses for delete merged branch
func RestoreBranchPost(ctx *context.Context) {
	defer redirect(ctx)

	branchID := ctx.QueryInt64("branch_id")
	branchName := ctx.Query("name")

	deletedBranch, err := ctx.Repo.Repository.GetDeletedBranchByID(branchID)
	if err != nil {
		log.Error("GetDeletedBranchByID: %v", err)
		ctx.Flash.Error(ctx.Tr("repo.branch.restore_failed", branchName))
		return
	}

	if err := ctx.Repo.GitRepo.CreateBranch(deletedBranch.Name, deletedBranch.Commit); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			ctx.Flash.Error(ctx.Tr("repo.branch.already_exists", deletedBranch.Name))
			return
		}
		log.Error("CreateBranch: %v", err)
		ctx.Flash.Error(ctx.Tr("repo.branch.restore_failed", deletedBranch.Name))
		return
	}

	if err := ctx.Repo.Repository.RemoveDeletedBranch(deletedBranch.ID); err != nil {
		log.Error("RemoveDeletedBranch: %v", err)
		ctx.Flash.Error(ctx.Tr("repo.branch.restore_failed", deletedBranch.Name))
		return
	}

	// Don't return error below this
	if err := repofiles.PushUpdate(
		ctx.Repo.Repository,
		deletedBranch.Name,
		repofiles.PushUpdateOptions{
			RefFullName:  git.BranchPrefix + deletedBranch.Name,
			OldCommitID:  git.EmptySHA,
			NewCommitID:  deletedBranch.Commit,
			PusherID:     ctx.User.ID,
			PusherName:   ctx.User.Name,
			RepoUserName: ctx.Repo.Owner.Name,
			RepoName:     ctx.Repo.Repository.Name,
		}); err != nil {
		log.Error("Update: %v", err)
	}

	ctx.Flash.Success(ctx.Tr("repo.branch.restore_success", deletedBranch.Name))
}

func redirect(ctx *context.Context) {
	ctx.JSON(200, map[string]interface{}{
		"redirect": ctx.Repo.RepoLink + "/branches",
	})
}

func deleteBranch(ctx *context.Context, branchName string) error {
	commit, err := ctx.Repo.GitRepo.GetBranchCommit(branchName)
	if err != nil {
		log.Error("GetBranchCommit: %v", err)
		return err
	}

	if err := ctx.Repo.GitRepo.DeleteBranch(branchName, git.DeleteBranchOptions{
		Force: true,
	}); err != nil {
		log.Error("DeleteBranch: %v", err)
		return err
	}

	// Don't return error below this
	if err := repofiles.PushUpdate(
		ctx.Repo.Repository,
		branchName,
		repofiles.PushUpdateOptions{
			RefFullName:  git.BranchPrefix + branchName,
			OldCommitID:  commit.ID.String(),
			NewCommitID:  git.EmptySHA,
			PusherID:     ctx.User.ID,
			PusherName:   ctx.User.Name,
			RepoUserName: ctx.Repo.Owner.Name,
			RepoName:     ctx.Repo.Repository.Name,
		}); err != nil {
		log.Error("Update: %v", err)
	}

	if err := ctx.Repo.Repository.AddDeletedBranch(branchName, commit.ID.String(), ctx.User.ID); err != nil {
		log.Warn("AddDeletedBranch: %v", err)
	}

	return nil
}

func loadBranches(ctx *context.Context, page int, count int) ([]*Branch, int) {
	rawBranchesAll, err := repo_module.GetBranches(ctx.Repo.Repository)
	if err != nil {
		ctx.ServerError("GetBranches", err)
		return nil, 0
	}

	offset := (page - 1) * count
	if offset > len(rawBranchesAll) {
		return []*Branch{}, 0
	}
	if offset + count > len(rawBranchesAll) {
		count = len(rawBranchesAll) - offset
	}
	rawBranches := rawBranchesAll[offset:offset + count]

	protectedBranches, err := ctx.Repo.Repository.GetProtectedBranches()
	if err != nil {
		ctx.ServerError("GetProtectedBranches", err)
		return nil, 0
	}

	repoIDToRepo := map[int64]*models.Repository{}
	repoIDToRepo[ctx.Repo.Repository.ID] = ctx.Repo.Repository

	repoIDToGitRepo := map[int64]*git.Repository{}
	repoIDToGitRepo[ctx.Repo.Repository.ID] = ctx.Repo.GitRepo

	branches := make([]*Branch, len(rawBranches))
	for i := range rawBranches {
		commit, err := rawBranches[i].GetCommit()
		if err != nil {
			ctx.ServerError("GetCommit", err)
			return nil, 0
		}

		var isProtected bool
		branchName := rawBranches[i].Name
		for _, b := range protectedBranches {
			if b.BranchName == branchName {
				isProtected = true
				break
			}
		}

		divergence, divergenceError := repofiles.CountDivergingCommits(ctx.Repo.Repository, branchName)
		if divergenceError != nil {
			ctx.ServerError("CountDivergingCommits", divergenceError)
			return nil, 0
		}

		pr, err := models.GetLatestPullRequestByHeadInfo(ctx.Repo.Repository.ID, branchName)
		if err != nil {
			ctx.ServerError("GetLatestPullRequestByHeadInfo", err)
			return nil, 0
		}
		headCommit := commit.ID.String()

		mergeMovedOn := false
		if pr != nil {
			pr.HeadRepo = ctx.Repo.Repository
			if err := pr.LoadIssue(); err != nil {
				ctx.ServerError("pr.LoadIssue", err)
				return nil, 0
			}
			if repo, ok := repoIDToRepo[pr.BaseRepoID]; ok {
				pr.BaseRepo = repo
			} else if err := pr.LoadBaseRepo(); err != nil {
				ctx.ServerError("pr.LoadBaseRepo", err)
				return nil, 0
			} else {
				repoIDToRepo[pr.BaseRepoID] = pr.BaseRepo
			}
			pr.Issue.Repo = pr.BaseRepo

			if pr.HasMerged {
				baseGitRepo, ok := repoIDToGitRepo[pr.BaseRepoID]
				if !ok {
					baseGitRepo, err = git.OpenRepository(pr.BaseRepo.RepoPath())
					if err != nil {
						ctx.ServerError("OpenRepository", err)
						return nil, 0
					}
					defer baseGitRepo.Close()
					repoIDToGitRepo[pr.BaseRepoID] = baseGitRepo
				}
				pullCommit, err := baseGitRepo.GetRefCommitID(pr.GetGitRefName())
				if err != nil && !git.IsErrNotExist(err) {
					ctx.ServerError("GetBranchCommitID", err)
					return nil, 0
				}
				if err == nil && headCommit != pullCommit {
					// the head has moved on from the merge - we shouldn't delete
					mergeMovedOn = true
				}
			}
		}

		isIncluded := divergence.Ahead == 0 && ctx.Repo.Repository.DefaultBranch != branchName

		branches[i] = &Branch{
			Name:              branchName,
			Commit:            commit,
			IsProtected:       isProtected,
			IsIncluded:        isIncluded,
			CommitsAhead:      divergence.Ahead,
			CommitsBehind:     divergence.Behind,
			LatestPullRequest: pr,
			MergeMovedOn:      mergeMovedOn,
		}
	}

	if ctx.Repo.CanWrite(models.UnitTypeCode) {
		deletedBranches, err := getDeletedBranches(ctx)
		if err != nil {
			ctx.ServerError("getDeletedBranches", err)
			return nil, 0
		}
		branches = append(branches, deletedBranches...)
	}

	return branches, len(rawBranchesAll)
}

func getDeletedBranches(ctx *context.Context) ([]*Branch, error) {
	branches := []*Branch{}

	deletedBranches, err := ctx.Repo.Repository.GetDeletedBranches()
	if err != nil {
		return branches, err
	}

	for i := range deletedBranches {
		deletedBranches[i].LoadUser()
		branches = append(branches, &Branch{
			Name:          deletedBranches[i].Name,
			IsDeleted:     true,
			DeletedBranch: deletedBranches[i],
		})
	}

	return branches, nil
}

// CreateBranch creates new branch in repository
func CreateBranch(ctx *context.Context, form auth.NewBranchForm) {
	if !ctx.Repo.CanCreateBranch() {
		ctx.NotFound("CreateBranch", nil)
		return
	}

	if ctx.HasError() {
		ctx.Flash.Error(ctx.GetErrMsg())
		ctx.Redirect(ctx.Repo.RepoLink + "/src/" + ctx.Repo.BranchNameSubURL())
		return
	}

	var err error
	if ctx.Repo.IsViewBranch {
		err = repo_module.CreateNewBranch(ctx.User, ctx.Repo.Repository, ctx.Repo.BranchName, form.NewBranchName)
	} else {
		err = repo_module.CreateNewBranchFromCommit(ctx.User, ctx.Repo.Repository, ctx.Repo.BranchName, form.NewBranchName)
	}
	if err != nil {
		if models.IsErrTagAlreadyExists(err) {
			e := err.(models.ErrTagAlreadyExists)
			ctx.Flash.Error(ctx.Tr("repo.branch.tag_collision", e.TagName))
			ctx.Redirect(ctx.Repo.RepoLink + "/src/" + ctx.Repo.BranchNameSubURL())
			return
		}
		if models.IsErrBranchAlreadyExists(err) || git.IsErrPushOutOfDate(err) {
			ctx.Flash.Error(ctx.Tr("repo.branch.branch_already_exists", form.NewBranchName))
			ctx.Redirect(ctx.Repo.RepoLink + "/src/" + ctx.Repo.BranchNameSubURL())
			return
		}
		if models.IsErrBranchNameConflict(err) {
			e := err.(models.ErrBranchNameConflict)
			ctx.Flash.Error(ctx.Tr("repo.branch.branch_name_conflict", form.NewBranchName, e.BranchName))
			ctx.Redirect(ctx.Repo.RepoLink + "/src/" + ctx.Repo.BranchNameSubURL())
			return
		}
		if git.IsErrPushRejected(err) {
			e := err.(*git.ErrPushRejected)
			if len(e.Message) == 0 {
				ctx.Flash.Error(ctx.Tr("repo.editor.push_rejected_no_message"))
			} else {
				ctx.Flash.Error(ctx.Tr("repo.editor.push_rejected", utils.SanitizeFlashErrorString(e.Message)))
			}
			ctx.Redirect(ctx.Repo.RepoLink + "/src/" + ctx.Repo.BranchNameSubURL())
			return
		}

		ctx.ServerError("CreateNewBranch", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("repo.branch.create_success", form.NewBranchName))
	ctx.Redirect(ctx.Repo.RepoLink + "/src/branch/" + util.PathEscapeSegments(form.NewBranchName))
}
