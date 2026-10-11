# API proxykenny

Une seule route utile : l'URL de l'image, qui contient la signature, les options et la source.
Le serveur répond par l'image transformée, ou par un code d'erreur explicite.

## Adresses

| Port | Chemin | Rôle |
|---|---|---|
| `8080` (`KP_BIND`) | `/<signature>/<options…>/plain/<source>[@<format>]` | image transformée |
| `8080` | `/<signature>/<options…>/<source en base64url>[.<format>]` | idem, source encodée |
| `8080` | `/health`, `/healthz` | état du service (`200 ok`) |
| `8081` (`KP_METRICS_BIND`) | `/metrics` | métriques Prometheus (à garder sur un réseau interne) |

Méthodes acceptées : `GET` et `HEAD`.

## Exemples

```bash
# remplir un carré 400×400 centré sur les visages, en WebP
curl -o avatar.webp "http://localhost:8080/<sig>/rs:fill:400:400/g:face/f:webp/plain/https://exemple.com/portrait.jpg"

# flouter tous les visages, meilleur format pour le navigateur
curl -H "Accept: image/avif,image/webp" -o flou "http://localhost:8080/<sig>/w:1200/bf:12/f:auto/plain/https://exemple.com/foule.heic"

# en-têtes seulement
curl -I "http://localhost:8080/<sig>/w:800/plain/https://exemple.com/photo.jpg"
```

La liste complète des options (`rs`, `g`, `c`, `fc`, `fd`, `bl`, `sh`, `pix`, `bf`, `pf`, `rot`, `q`, `f`, `bg`, `pg`, `dpr`…)
est dans le [README](../README.md#format-des-url).

## Réponse réussie

| En-tête | Valeur |
|---|---|
| `Content-Type` | format produit (`image/avif`, `image/webp`, `image/jpeg`…) |
| `Cache-Control` | `public, max-age=31536000, immutable` — une URL donne toujours la même image |
| `ETag` | empreinte de la réponse ; un `If-None-Match` identique renvoie `304 Not Modified` sans retraitement |
| `Vary` | `Accept` quand `f:auto` est utilisé |
| `Server-Timing` | `dl` (téléchargement), `proc` (traitement), `total`, en millisecondes |
| `X-Content-Type-Options` | `nosniff` |

## Erreurs

Corps en texte brut (`text/plain`, une ligne), jamais mis en cache (`Cache-Control: no-store`).
Chaque refus est compté dans `proxykenny_rejected_total{reason}`.

| Code | `reason` | Cause |
|---|---|---|
| 400 | `options` | option inconnue ou valeur hors bornes |
| 400 | `src_url` | URL source invalide |
| 403 | `signature` | signature absente ou fausse |
| 403 | `src_forbidden` | source interdite : IP interne (protection SSRF) ou domaine hors `KP_ALLOWED_HOSTS` |
| 404 | `path` / `src_not_found` | chemin invalide / image source introuvable |
| 405 | `method` | méthode autre que GET / HEAD |
| 413 | `src_too_large` | fichier source au-delà de `KP_MAX_SRC_MB` |
| 415 | `unsupported` | format source non pris en charge |
| 422 | `too_many_pixels` | image au-delà de `KP_MAX_SRC_MPIX` (anti « bombe de décompression ») |
| 502 | `src_error` | erreur du serveur d'origine |
| 503 | `overloaded` | file d'attente pleine (`KP_MAX_QUEUE`) ; en-tête `Retry-After: 1` |
| 503 | `queue_timeout` | trop longtemps dans la file |
| 504 | `timeout` / `src_timeout` | traitement ou téléchargement trop long (`KP_REQUEST_TIMEOUT`) |

## Signer une URL

`signature = base64url_sans_remplissage( HMAC-SHA256( clé, sel + chemin ) )` — clé et sel fournis en hexadécimal
(`KP_KEY`, `KP_SALT`), `chemin` = tout ce qui suit la signature, `/` initial compris.

**Avec le binaire**

```bash
proxykenny -sign '/rs:fill:300:300/g:face/plain/https://exemple.com/a.jpg'
```

**JavaScript (Node.js)**

```js
import { createHmac } from 'node:crypto';
const sign = (path, keyHex, saltHex) =>
  createHmac('sha256', Buffer.from(keyHex, 'hex'))
    .update(Buffer.concat([Buffer.from(saltHex, 'hex'), Buffer.from(path)]))
    .digest('base64url');
const path = '/rs:fill:300:300/g:face/plain/https://exemple.com/a.jpg';
const url = `https://images.exemple.com/${sign(path, process.env.KP_KEY, process.env.KP_SALT)}${path}`;
```

**Python**

```python
import base64, hashlib, hmac, os
def sign(path: str) -> str:
    key, salt = bytes.fromhex(os.environ["KP_KEY"]), bytes.fromhex(os.environ["KP_SALT"])
    mac = hmac.new(key, salt + path.encode(), hashlib.sha256).digest()
    return base64.urlsafe_b64encode(mac).rstrip(b"=").decode()
```

**PHP**

```php
function sign(string $path): string {
    $mac = hash_hmac('sha256', hex2bin(getenv('KP_SALT')) . $path, hex2bin(getenv('KP_KEY')), true);
    return rtrim(strtr(base64_encode($mac), '+/', '-_'), '=');
}
```

La signature se calcule **côté serveur** de votre site : la clé ne doit jamais être envoyée au navigateur.
En développement uniquement, `KP_ALLOW_UNSIGNED=true` accepte le mot `unsafe` à la place de la signature.
