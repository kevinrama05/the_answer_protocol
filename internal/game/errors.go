package game

// Error replies. Codes from RFC 42TAP section 8.2 come first; the others are
// extensions documented in the README ("Protocol Implementation").
const (
	errNameInUse       = "ERR 201 NAME_IN_USE"
	errNoExit          = "ERR 301 NO_EXIT"
	errNotInGroup      = "ERR 401 NOT_IN_GROUP"
	errAlreadyInGroup  = "ERR 402 ALREADY_IN_GROUP"
	errItemNotFound    = "ERR 404 ITEM_NOT_FOUND"
	errItemNotInInv    = "ERR 404 ITEM_NOT_IN_INVENTORY"
	errNPCNotFound     = "ERR 404 NPC_NOT_FOUND"
	errNPCNotHostile   = "ERR 405 NPC_NOT_HOSTILE"
	errNoQuest         = "ERR 406 NO_QUEST_AVAILABLE"
	ErrConnectionFail  = "ERR 900 CONNECTION_FAILED"
	errUnknownCommand  = "ERR 100 UNKNOWN_COMMAND"
	errInvalidArgs     = "ERR 101 INVALID_ARGUMENTS"
	ErrNotAuth         = "ERR 102 NOT_AUTHENTICATED"
	ErrAlreadyAuth     = "ERR 103 ALREADY_AUTHENTICATED"
	ErrLineTooLong     = "ERR 104 LINE_TOO_LONG"
	ErrRateLimited     = "ERR 105 RATE_LIMITED"
	ErrInvalidEncoding = "ERR 106 INVALID_ENCODING"
	errInvalidName     = "ERR 202 INVALID_NAME"
	errPlayerNotFound  = "ERR 403 PLAYER_NOT_FOUND"
	errNotInvited      = "ERR 407 NOT_INVITED"
	errNotObtainable   = "ERR 408 ITEM_NOT_OBTAINABLE"
	errInventoryFull   = "ERR 409 INVENTORY_FULL"
	errNotInCombat     = "ERR 410 NOT_IN_COMBAT"
	errNotUsable       = "ERR 411 ITEM_NOT_USABLE"
)
