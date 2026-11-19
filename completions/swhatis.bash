# bash completion for swhatis

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
