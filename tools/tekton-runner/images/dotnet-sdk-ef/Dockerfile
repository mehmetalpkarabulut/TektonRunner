FROM mcr.microsoft.com/dotnet/sdk:8.0

ENV PATH="$PATH:/root/.dotnet/tools"

# Install EF CLI globally so migration jobs can run "dotnet ef ..."
RUN dotnet tool install --global dotnet-ef --version 8.*

