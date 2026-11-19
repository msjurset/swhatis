package main

const zshCompletion = `#compdef swhatis

_swhatis() {
  local context state state_descr line
  typeset -A opt_args

  _arguments -C \
    '-w[Force web/search lookup even if local info exists]' \
    '-ai[Force search-grounded Gemini summary via 1Password/Keychain]' \
    '-no-cache[Bypass local web summary cache]' \
    '-no-help[Skip --help first-line fallback probe]' \
    '-json[Output structured JSON]' \
    '(- *)-auth[Authenticate with 1Password and cache Gemini API key in Keychain]' \
    '(- *)-clear-auth[Remove cached Gemini API key from Keychain]' \
    '-op-ref[1Password op:// reference for Gemini API key]:op reference:' \
    '(- *)-completion[Print shell completion script]:shell:(zsh bash)' \
    '(- *)-version[Print version and exit]' \
    '(- *)-h[Show help]' \
    '(- *)--help[Show help]' \
    '*:command:_command_names -e'
}

_swhatis "$@"
`

const bashCompletion = `# bash completion for swhatis

_swhatis() {
  local cur prev flags
  COMPREPLY=()
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"
  flags="-w -ai -no-cache -no-help -json -auth -clear-auth -op-ref -completion -version -h --help"

  case "$prev" in
    -completion)
      COMPREPLY=( $(compgen -W "zsh bash" -- "$cur") )
      return 0
      ;;
    -op-ref)
      return 0
      ;;
  esac

  if [[ "$cur" == -* ]]; then
    COMPREPLY=( $(compgen -W "$flags" -- "$cur") )
    return 0
  fi

  COMPREPLY=( $(compgen -c -- "$cur") )
}

complete -F _swhatis swhatis
`
