# Image du Control Plane : interface Vue intégrée, runtime Java inclus.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src/control-plane
COPY control-plane/pom.xml ./
RUN mvn -B -q dependency:go-offline
COPY control-plane/src ./src
COPY --from=web /src/web/dist ./src/main/resources/META-INF/resources
RUN mvn -B -q package -DskipTests

FROM eclipse-temurin:21-jre-alpine
RUN addgroup -S prm && adduser -S -G prm -H prm
WORKDIR /opt/prm
COPY --from=build --chown=prm:prm /src/control-plane/target/quarkus-app/ ./
USER prm
EXPOSE 9080 9443
ENV PRM_HTTP_HOST=0.0.0.0
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s CMD wget -qO- http://127.0.0.1:9080/health/live || exit 1
ENTRYPOINT ["java", "-XX:+ExitOnOutOfMemoryError", "-jar", "quarkus-run.jar"]
