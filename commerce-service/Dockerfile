FROM mcr.microsoft.com/dotnet/sdk:10.0 AS build
ENV DOTNET_CLI_TELEMETRY_OPTOUT=1
WORKDIR /src
COPY commerce-service/ ./
COPY gateway/api/order.proto /gateway/api/order.proto
COPY contracts/pricing.proto /contracts/pricing.proto
RUN dotnet restore Commerce.sln
RUN dotnet publish src/Commerce.Api/Commerce.Api.csproj -c Release -o /app/publish --no-restore

FROM mcr.microsoft.com/dotnet/aspnet:10.0
ENV DOTNET_CLI_TELEMETRY_OPTOUT=1
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends wget && rm -rf /var/lib/apt/lists/*
COPY --from=build /app/publish ./
RUN chown -R app:app /app
USER app
EXPOSE 50051 8087
ENTRYPOINT ["dotnet", "Commerce.Api.dll"]
