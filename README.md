# proxykenny

Serveur de traitement d'images à la volée, inspiré d'[imgproxy](https://github.com/imgproxy/imgproxy) :
Go + [libvips](https://www.libvips.org/) (via govips), détection de visages avec le réseau de neurones
[YuNet](https://github.com/opencv/opencv_zoo/tree/main/models/face_detection_yunet) (ONNX Runtime), [pigo](https://github.com/esimov/pigo) en secours.

- **Tous les formats** : JPEG, PNG, WebP, AVIF, HEIC/HEIF, JPEG XL, GIF (animé), TIFF, SVG, PDF, BMP, ICO, PSD,
  JPEG 2000, OpenEXR… (libvips + ImageMagick en secours).
- **Rapide et sobre** : réduction pendant le décodage (shrink-on-load), traitement en flux par libvips,
  1 thread libvips par image et N workers en parallèle, jemalloc, ~25 Mo de RAM au repos.
- **Protège la machine** : limites de taille et de pixels (anti « bombe de décompression »), file d'attente bornée
  (503 au-delà), délais, blocage des IP internes (SSRF), conteneur en lecture seule, sans privilège, non-root.
- **Observable** : métriques Prometheus sur `:8081/metrics`, en-tête `Server-Timing` sur chaque réponse.

## Architecture

```
navigateur ──► proxykenny :8080 ──► source (https://… ou local:///)
                 │  1. signature HMAC   2. options   3. file bornée (503 si saturé)
                 │  4. téléchargement   5. libvips (+ YuNet)   6. encodage
                 └─► /metrics :8081 ──► Prometheus ──► Grafana
pklab :8090 (démo, bancs d'essai) ──► proxykenny
```

| Dossier | Rôle |
|---|---|
| `cmd/proxykenny` | binaire de l'API (`-sign` pour signer une URL, `-healthcheck` pour Docker) |
| `cmd/pklab` | binaire du lab de démonstration |
| `internal/config` | configuration par variables d'environnement `KP_*` |
| `internal/options` | lecture des URL (options `rs:`, `g:`, `fc:`…) et bornes de sécurité |
| `internal/security` | signature HMAC des URL |
| `internal/fetch` | téléchargement des sources : taille max, délais, blocage SSRF, `local:///` |
| `internal/process` | traitement libvips : `process.go` (pipeline), `load.go` (chargement), `gravity.go` (cadrage), `faces.go` (visages), `effects.go`, `export.go` (encodage) |
| `internal/face` | détection de visages : `yunet.go` (réseau de neurones, principal), `face.go` (pigo, secours) |
| `internal/server` | serveur HTTP : file d'attente, codes d'erreur, cache (ETag), métriques |
| `internal/metrics` | métriques Prometheus `proxykenny_*` |
| `internal/lab` | lab : galerie, bac à sable, bancs d'essai et campagnes |
| `testdata/` | photos de test (NASA, domaine public) |

## Format des URL

```
http://<hôte>:8080/<signature>/<option>/<option>/.../plain/<url source>[@<format>]
http://<hôte>:8080/<signature>/<option>/<option>/.../<url source en base64url>[.<format>]
```

Sources : `https://…`, ou `local:///chemin` (dossier monté dans `/images`).
Une URL source contenant `?` ou `#` doit être encodée en base64url.

| Option | Exemple | Effet |
|---|---|---|
| `rs:<type>:<l>:<h>:<agrandir>` | `rs:fill:300:200` | redimensionne ; types `fit`, `fill`, `force`, `auto` |
| `w:<l>` / `h:<h>` / `s:<l>:<h>` | `w:800` | largeur / hauteur / les deux |
| `el:1` | | autorise l'agrandissement |
| `dpr:<n>` | `dpr:2` | multiplie les dimensions (écrans haute densité) |
| `g:<gravité>` | `g:face`, `g:sm`, `g:soea`, `g:fp:0.3:0.6` | point d'ancrage du recadrage : visages, zone intéressante, boussole, point focal |
| `c:<l>:<h>[:<gravité>]` | `c:800:600:face`, `c:0.5:1:we` | recadre la source avant redimensionnement ; valeurs ≤ 1 = proportion de l'image |
| `fc:<marge>[:largest]` | `rs:fill:256:256/fc:0.4:largest` | **zoom sur les visages** (avatar, portrait) : marge relative autour des visages ; `largest` = visage principal seulement ; taille exacte garantie |
| `fd` | `w:1000/fd` | **encadre les visages détectés** (contrôle / démonstration) |
| `bl:<sigma>` | `bl:5` | flou gaussien |
| `sh:<sigma>` | `sh:1` | netteté |
| `pix:<taille>` | `pix:16` | pixellisation |
| `bf:<sigma>` | `bf:10` | **floute les visages** (anonymisation) |
| `pf:<taille>` | `pf:12` | **pixellise les visages** |
| `rot:<angle>` | `rot:90` | rotation (90, 180, 270) |
| `q:<1-100>` | `q:75` | qualité |
| `f:<format>` | `f:avif`, `f:auto` | format de sortie ; `auto` = selon l'en-tête `Accept` du navigateur |
| `bg:<rrggbb>` | `bg:ffffff` | fond pour aplatir la transparence (JPEG) |
| `pg:<n>` | `pg:2` | page (PDF, TIFF multipage) |
| `ar:0` / `sm:0` | | désactive la rotation EXIF / garde les métadonnées |

Exemples :

```
/<sig>/rs:fill:400:400/g:face/f:webp/plain/https://exemple.com/portrait.jpg
/<sig>/w:1200/bf:12/f:auto/plain/https://exemple.com/foule.heic
/<sig>/rs:fit:300:300/plain/local:///anim.gif            ← animation conservée
```

## Visages

- **YuNet** (OpenCV Zoo, licence MIT, modèle de 230 Ko embarqué dans le binaire) exécuté par
  **ONNX Runtime 1.29** (téléchargé et vérifié par SHA-256 dans le `Dockerfile`), 1 thread par détection,
  sur une copie de l'image réduite à 640 px. Détecte les petits visages (~10 px), les têtes inclinées, les profils
  partiels et les visages en partie couverts. Mesuré : ~20 ms par image.
- **pigo** en secours (`KP_FACE_ENGINE=pigo`, ou automatiquement si ONNX Runtime est absent — un test de la CI
  vérifie que YuNet est bien actif).
- Floutage / pixellisation : la zone couvre toute la tête et se fond dans l'image (ellipse à bords progressifs).
- Centrage `g:face` : sur l'ensemble des visages, ou sur le visage principal si le groupe ne tient pas dans le cadre.

## Signature

`signature = base64url( HMAC-SHA256( KP_KEY, KP_SALT + chemin ) )`, clé et sel en hexadécimal.
Le binaire sait signer :

```bash
docker exec proxykenny proxykenny -sign '/rs:fill:300:300/g:face/plain/https://exemple.com/a.jpg'
```

## Configuration (variables d'environnement)

| Variable | Défaut | Rôle |
|---|---|---|
| `KP_KEY`, `KP_SALT` | — | clé et sel HMAC (hex) |
| `KP_WORKERS` | nb de cœurs | traitements simultanés |
| `KP_MAX_QUEUE` | 64 | requêtes en attente avant 503 |
| `KP_MAX_SRC_MB` | 30 | taille max du fichier source |
| `KP_MAX_SRC_MPIX` | 60 | pixels max de la source (mégapixels) |
| `KP_MAX_DST_DIM` | 8192 | dimension max demandée |
| `KP_REQUEST_TIMEOUT` | 20s | délai max d'une requête |
| `KP_REJECT_DELAY` | 250ms | attente avant un 503 (freine les clients qui insistent, préserve le CPU) |
| `KP_ALLOWED_HOSTS` | (tous) | domaines sources autorisés, ex. `.exemple.com,cdn.site.fr` |
| `KP_ALLOW_PRIVATE_SOURCES` | false | autorise les IP internes (désactivé = protection SSRF) |
| `KP_LOCAL_ROOT` | (désactivé) | dossier des sources `local:///` |
| `KP_AUTO_FORMATS` | `webp,avif` | préférence pour `f:auto` |
| `KP_FACE_ENGINE` | yunet | moteur de visages : `yunet` (réseau de neurones) ou `pigo` (secours, si ONNX Runtime absent) |
| `KP_FACE_SCORE` | 0.6 | YuNet : score minimal d'un visage (0..1) |
| `KP_FACE_MIN_QUALITY` | 30 | score minimal d'un visage (mesuré : vrais visages > 100, faux positifs < 20) |
| `KP_FACE_TILT` | fallback | têtes inclinées (±18° et ±32°) : `fallback` = seulement si aucun visage droit trouvé, `always`, `off` |
| `KP_*_QUALITY`, `KP_*_EFFORT` | | qualité / effort par format (AVIF effort 2 = rapide) |
| `KP_ALLOW_UNSIGNED` | false | accepte la signature `unsafe` (développement uniquement) |

## proxykenny lab (web app de démonstration)

`cmd/pklab` — interface web sur le port 8090, protégée par mot de passe (`LAB_PASSWORD`, utilisateur `admin`).

- **Galerie** : images du dossier monté dans `/images`, envoi par glisser-déposer (dossier `uploads/`).
- **Fiche image** : 14 transformations côte à côte avec format, poids, gain vs source, temps serveur, visages détectés.
- **Bac à sable** : formulaire d'options → résultat, mesures et URL signée prête à copier.
- **Bancs d'essai** :
  - **campagne** (recommandé) : 19 tests enchaînés en arrière-plan, un seul paramètre varie par groupe —
    taille de sortie (320 → 2400 px), format (JPEG, WebP, AVIF, JPEG XL), opération (redimensionner, smart,
    visage, avatar, floutage), clients simultanés (1 → 32) — avec barres et conclusions automatiques ;
  - **test unique** : un scénario, 1 à 64 clients, 5 à 30 s.
  - « Clients simultanés » = nombre de requêtes en cours en même temps. Avec 3 workers, le débit maximal est
    atteint vers 3-4 clients ; au-delà les requêtes attendent leur tour (la latence augmente, le débit plafonne).
- Métriques `pklab_*` sur `:8091/metrics` ; image distroless de ~24 Mo, plafonnée à 0,5 cœur / 256 Mo.

## Métriques (Prometheus)

Un tableau de bord Grafana se branche directement sur ces métriques.

| Métrique | Contenu |
|---|---|
| `proxykenny_requests_total{code}` | requêtes par code HTTP |
| `proxykenny_duration_seconds{stage}` | durée par étape : `download`, `queue`, `process`, `total` (images servies), `rejected` (refus) |
| `proxykenny_images_total{src,out}` | images produites par format source et de sortie |
| `proxykenny_source_bytes`, `proxykenny_output_bytes` | tailles des sources et des images produites |
| `proxykenny_processing`, `proxykenny_queue_waiting` | travaux en cours / en attente |
| `proxykenny_rejected_total{reason}` | refus par motif (`overloaded`, `signature`, `src_*`, `options`…) |
| `proxykenny_face_detect_seconds{engine}`, `proxykenny_faces_detected_total` | détection de visages |
| `proxykenny_vips_memory_bytes` | mémoire de libvips |
| `pklab_campaign_step_rps{group,step}`, `pklab_campaign_step_p50_seconds` | résultats de la dernière campagne |

## Développement

```bash
docker build --target test .      # gofmt + go vet + staticcheck + tests + benchmarks
docker build --target runtime -t proxykenny .
docker run --rm -p 8080:8080 -e KP_ALLOW_UNSIGNED=true proxykenny
```

Tests : options, signature, SSRF, serveur HTTP (signature, cache 304, refus, surcharge → 503), 13 formats
d'entrée et 7 de sortie, toutes les opérations, visages sur vraies photos (7/7 sur une photo de groupe, têtes
inclinées ±30°, petits visages), floutage sans bord rectangulaire, lab et campagnes.

## Démarrage rapide

```bash
docker build --target runtime -t proxykenny .
docker run --rm -p 8080:8080 -p 8081:8081 \
  -e KP_KEY=$(openssl rand -hex 32) -e KP_SALT=$(openssl rand -hex 16) proxykenny
# signer une URL :
docker exec <conteneur> proxykenny -sign '/rs:fill:300:300/g:face/plain/https://exemple.com/a.jpg'
```

## Licence

Code source ouvert sous **[PolyForm Noncommercial 1.0.0](LICENSE)** : vous pouvez lire, utiliser, modifier et
partager proxykenny librement **pour tout usage non commercial** (personnel, étude, recherche, associations,
organismes publics…).

**Tout usage commercial** — l'intégrer à un produit ou service payant, l'exploiter pour une entreprise, le revendre
ou le proposer en ligne contre rémunération — **nécessite une licence commerciale**.
Contact : **contact@unknownproject.fr**.

© 2026 unknownproject — tous droits réservés hors licence ci-dessus.
