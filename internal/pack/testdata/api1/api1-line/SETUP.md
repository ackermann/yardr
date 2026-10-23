# Setting up api1-line

The fixture flow pack of pack API 1. It is applied after the api1-source
pack, which declares the acme source, and after a hook receives acme:

```sh
yardr pack apply api1-source
yardr hook add acme --source acme --secret-file ~/.yardr/acme.secret
yardr pack apply api1-line --depot web
```

The import asks for the agents that run the builders and the assembly's
script runs as the yard's user. Nothing here is a secret.
