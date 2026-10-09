package wizard

import "github.com/CharlesMod/wasp/profile"

func profileRoot() string { return profile.InstallRoot() }

func detectProfile(o Options) profile.Profile {
	if o.Profile != nil {
		return *o.Profile
	}
	return profile.Detect()
}
