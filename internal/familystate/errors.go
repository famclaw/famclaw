// Package familystate provides a structured, parent-managed store of
// per-family facts (allergies, pets, important dates, etc.) plus a
// prompt-injection snapshot for safety-critical categories.
package familystate

import "errors"

// ErrBuiltinCategory is returned when a caller tries to delete a
// category that ships with the bot (allergies, dietary_restrictions,
// important_dates, pets). These rows have is_builtin=1 in the
// family_fact_categories table.
var ErrBuiltinCategory = errors.New("family_state: cannot delete a built-in category")

// ErrUnknownCategory is returned when a fact references a category
// that does not exist in family_fact_categories.
var ErrUnknownCategory = errors.New("family_state: unknown category")

// ErrUnknownSubject is returned when a fact's subject does not match
// any name in config.Users and is not the literal 'family'.
var ErrUnknownSubject = errors.New("family_state: subject must be a configured family member or 'family'")

// ErrCategoryNotEmpty is returned when a caller tries to delete a
// category that still has at least one family_facts row referencing it.
// The FK constraint on family_facts(category) is RESTRICT.
var ErrCategoryNotEmpty = errors.New("family_state: category has facts; delete them first")

// ErrLengthCap is returned by handler-side validation when a label
// or value exceeds the documented cap (label ≤ 64, value ≤ 512,
// category.name ≤ 32, category.description ≤ 256).
var ErrLengthCap = errors.New("family_state: input exceeds length cap")
