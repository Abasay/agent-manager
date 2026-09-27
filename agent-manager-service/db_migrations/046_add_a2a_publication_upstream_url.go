// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package dbmigrations

import (
	"gorm.io/gorm"
)

// a2a_publications records the upstream URL each published row gave its
// gateway, so the reconciler can detect a binding whose Service address moved.
// Existing rows start empty and are republished once by the first drift check.
var migration046 = migration{
	ID: 46,
	Migrate: func(db *gorm.DB) error {
		return db.Exec(`
		ALTER TABLE a2a_publications
			ADD COLUMN IF NOT EXISTS published_upstream_url TEXT NOT NULL DEFAULT '';
		`).Error
	},
}
