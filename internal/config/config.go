package config

// MaxParticipantsPerDiscussion caps the number of DISTINCT non-owner users who
// may join a discussion via a share link. The owner never occupies a slot, so a
// shared discussion supports the owner plus this many participants. The
// (MaxParticipantsPerDiscussion+1)-th distinct joiner is rejected.
const MaxParticipantsPerDiscussion = 20
