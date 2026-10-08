// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package play

import (
	"context"
	"sync"

	"google.golang.org/api/androidpublisher/v3"
)

// lockPackage takes the edit lock of one app and returns its unlock function.
func (c *Client) lockPackage(packageName string) func() {
	value, _ := c.editLocks.LoadOrStore(packageName, &sync.Mutex{})
	mutex, _ := value.(*sync.Mutex)
	mutex.Lock()

	return mutex.Unlock
}

// ReadEdit opens an edit of the app, runs fn with its id and deletes the edit.
// Use it for calls that only read.
func (c *Client) ReadEdit(ctx context.Context, packageName string, fn func(editID string) error) error {
	return c.withEdit(ctx, packageName, false, fn)
}

// CommitEdit opens an edit of the app, runs fn with its id and commits the
// edit. When fn or the commit fails the edit is deleted instead.
func (c *Client) CommitEdit(ctx context.Context, packageName string, fn func(editID string) error) error {
	return c.withEdit(ctx, packageName, true, fn)
}

func (c *Client) withEdit(ctx context.Context, packageName string, commit bool, fn func(editID string) error) error {
	defer c.lockPackage(packageName)()

	edit, err := c.Service.Edits.Insert(packageName, &androidpublisher.AppEdit{}).Context(ctx).Do()
	if err != nil {
		return err
	}

	// An edit left open lingers until it expires. The delete is best effort and
	// deliberately ignores the caller's cancellation, and its own failure: the
	// error worth reporting is the one that led here.
	discard := func() {
		_ = c.Service.Edits.Delete(packageName, edit.Id).Context(context.WithoutCancel(ctx)).Do()
	}

	if err := fn(edit.Id); err != nil {
		discard()

		return err
	}

	if !commit {
		discard()

		return nil
	}

	if _, err := c.Service.Edits.Commit(packageName, edit.Id).Context(ctx).Do(); err != nil {
		discard()

		return err
	}

	return nil
}
