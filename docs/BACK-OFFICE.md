# Back-office : proxykenny lab

Application web d'administration et de démonstration, livrée à côté de l'API (`cmd/pklab`, image Docker `lab`).
Elle parle à l'API comme n'importe quel client : elle signe les URL avec la même clé.

## Lancer

```bash
docker build --target runtime -t proxykenny .
docker build --target lab -t proxykenny-lab .

docker network create pk
docker run -d --name proxykenny --network pk -e KP_KEY=… -e KP_SALT=… -v "$PWD/images:/images:ro" \
  -e KP_LOCAL_ROOT=/images -p 8080:8080 proxykenny
docker run -d --name pklab --network pk -e KP_KEY=… -e KP_SALT=… \
  -e LAB_PROXY_PUBLIC=http://localhost:8080 -e LAB_PASSWORD=… \
  -v "$PWD/images:/images" -p 8090:8090 proxykenny-lab
# → http://localhost:8090 (utilisateur admin)
```

`KP_KEY`, `KP_SALT` et `LAB_PROXY_PUBLIC` sont obligatoires ; sans `LAB_PASSWORD`, le lab démarre **sans
authentification** (un avertissement est écrit dans les journaux) : à réserver à un poste local.

## Réglages

| Variable | Défaut | Rôle |
|---|---|---|
| `KP_KEY`, `KP_SALT` | — | clé et sel de l'API (hex), pour signer les URL |
| `LAB_PROXY_PUBLIC` | — | adresse de l'API vue par le navigateur (images affichées) |
| `LAB_PROXY_INTERNAL` | `http://proxykenny:8080` | adresse de l'API vue par le lab (mesures, bancs d'essai) |
| `LAB_BIND` | `:8090` | adresse d'écoute de l'interface |
| `LAB_METRICS_BIND` | `:8091` | métriques Prometheus `pklab_*` |
| `LAB_IMAGES_DIR` | `/images` | dossier de la galerie (les envois vont dans `uploads/`) |
| `LAB_MAX_UPLOAD_MB` | `30` | taille maximale d'un envoi |
| `LAB_USER` / `LAB_PASSWORD` | `admin` / — | accès protégé (authentification HTTP Basic) |
| `LAB_GRAFANA_URL` | — | lien vers votre tableau de bord, affiché dans le lab |

## Pages

| Page | Contenu |
|---|---|
| `/` — **Galerie** | toutes les images du dossier, envoi par glisser-déposer (`POST /upload`) |
| `/image?f=<fichier>` — **Fiche image** | 14 transformations côte à côte : format, poids, gain par rapport à la source, temps serveur, visages détectés |
| `/play` — **Bac à sable** | formulaire d'options → résultat, mesures, et URL signée prête à copier |
| `/bench` — **Test unique** | un scénario de charge (1 à 64 clients, 5 à 30 s) : débit, latences p50 / p95 / p99, erreurs |
| `/campaign` — **Campagne** | 19 tests enchaînés en arrière-plan ; un seul paramètre varie par groupe (taille, format, opération, clients simultanés), avec graphiques et conclusions automatiques |

« Clients simultanés » = requêtes en cours en même temps. Le débit plafonne quand tous les workers de l'API sont
occupés ; au-delà, les requêtes attendent leur tour (la latence augmente) puis sont refusées en `503` quand la file
est pleine : c'est la protection de la machine, visible directement dans les campagnes.

## Supervision

Les résultats de la dernière campagne sont aussi exposés en métriques (`pklab_campaign_step_rps{group,step}`,
`pklab_campaign_step_p50_seconds`) pour être affichés dans Grafana à côté des métriques de l'API.
