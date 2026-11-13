package gateway

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode"

	"github.com/dnwandana/tether-agent/internal/skills"
)

// SkillCommand is one entry of the skill command table. Slug is the command name with
// "-". MenuName is the Telegram bot menu name, the slug with "-" changed to "_".
type SkillCommand struct {
	Slug, MenuName string
	Skill          skills.Skill
}

// ReservedCommandNames holds the command names that no skill gets, in addition to the
// command table. The channels handle these commands before the gateway: "exit" and "quit"
// on the CLI, and "start", "sethome", "approve", and "deny" on Telegram. The app registers
// "restart" only in the gateway role.
var ReservedCommandNames = []string{"exit", "quit", "start", "sethome", "approve", "deny", "restart"}

// reservedName reports whether name, or name with "-" changed to "_", or name with "_"
// changed to "-", is in builtins or in ReservedCommandNames.
func reservedName(name string, builtins map[string]Command) bool {
	for _, n := range []string{name, strings.ReplaceAll(name, "-", "_"), strings.ReplaceAll(name, "_", "-")} {
		if _, ok := builtins[n]; ok || slices.Contains(ReservedCommandNames, n) {
			return true
		}
	}
	return false
}

// SkillCommands returns the skill command table of list, in slug order. It walks the
// skills of the data dir first and then the external skills, each group in the order of
// list. The first skill with a slug wins.
func SkillCommands(list []skills.Skill, log *slog.Logger) []SkillCommand {
	if log == nil {
		log = slog.Default()
	}
	builtins := Commands()
	first := map[string]string{} // slug -> the skill name that has it
	var out []SkillCommand
	for _, external := range []bool{false, true} {
		for _, s := range list {
			if s.External != external {
				continue
			}
			slug := skills.CommandSlug(s.Name)
			// The name goes into the message between quotes and into a CLI line.
			if slug == "" || strings.ContainsFunc(s.Name, func(r rune) bool { return r == '"' || unicode.IsControl(r) }) {
				continue
			}
			if reservedName(slug, builtins) {
				log.Warn(fmt.Sprintf("skill command /%s is not available: a built-in command has the name; skill %s", slug, s.Name))
				continue
			}
			if name, ok := first[slug]; ok {
				log.Warn(fmt.Sprintf("skill command /%s is not available: skill %s has the same command; skill %s", slug, name, s.Name))
				continue
			}
			first[slug] = s.Name
			out = append(out, SkillCommand{Slug: slug, MenuName: strings.ReplaceAll(slug, "-", "_"), Skill: s})
		}
	}
	slices.SortFunc(out, func(a, b SkillCommand) int { return strings.Compare(a.Slug, b.Slug) })
	return out
}

// ResolveSkill returns the entry of cmds for the typed name. When no entry has the name,
// it tries the name with each "_" changed to "-", because Telegram menu names use "_".
func ResolveSkill(cmds []SkillCommand, name string) (SkillCommand, bool) {
	for _, n := range []string{name, strings.ReplaceAll(name, "_", "-")} {
		for _, c := range cmds {
			if c.Slug == n {
				return c, true
			}
		}
	}
	return SkillCommand{}, false
}

// SlashCandidate reports whether text is a slash command that can be a skill command: the
// command table does not have the name, and the name is not reserved. It reads no skill
// folder, so the gateway can call it in accept.
func SlashCandidate(text string) bool {
	name, _, ok := IsCommand(text)
	if !ok {
		return false
	}
	if _, found := lookupCommand(name); found {
		return false
	}
	return !reservedName(name, Commands())
}

// SkillCommandsFor returns the skill commands of the channel. A channel has skill
// commands only when skill_view is visible there, like the skills index of the system
// prompt. It reads the skill folders.
func (d *Deps) SkillCommandsFor(channel string) []SkillCommand {
	return d.skillCommands(channel, d.Toolsets(channel))
}

// SkillCommandsForSender returns the skill commands that a sender can run on the channel.
// An owner gets the commands of SkillCommandsFor. A member gets them only when the member
// toolsets show skill_view, because the member turn cannot load a skill otherwise.
func (d *Deps) SkillCommandsForSender(channel string, owner bool) []SkillCommand {
	if owner {
		return d.SkillCommandsFor(channel)
	}
	return d.skillCommands(channel, d.memberToolsets(channel))
}

func (d *Deps) skillCommands(channel string, sets map[string]bool) []SkillCommand {
	if d.Agent != nil && d.Agent.Tools != nil {
		visible, _ := d.Agent.Tools.Visible(sets, nil)
		if !visible["skill_view"] {
			return nil
		}
	}
	if d.Skills == nil {
		return nil
	}
	list, _, err := d.Skills()
	if err != nil {
		d.logger().Warn("cannot load the skills; no skill commands", "channel", channel, "err", err.Error())
		return nil
	}
	return SkillCommands(list, d.logger())
}
